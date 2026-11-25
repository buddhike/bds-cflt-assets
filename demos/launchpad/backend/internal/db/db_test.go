package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type row struct {
	PK string `dynamodbav:"pk"`
	SK string `dynamodbav:"sk"`
	N  int64  `dynamodbav:"n"`
	S  string `dynamodbav:"s,omitempty"`
}

var ctx = context.Background()

// backends are the DBs the contract tests run against. Memory is always there.
// With LAUNCHPAD_DYNAMODB_ENDPOINT set to a DynamoDB Local (or any DynamoDB
// endpoint that accepts the test credentials) the real implementation runs
// too, which is what keeps Memory honest:
//
//	docker run -d --rm -p 8000:8000 amazon/dynamodb-local -jar DynamoDBLocal.jar -inMemory
//	LAUNCHPAD_DYNAMODB_ENDPOINT=http://localhost:8000 go test ./internal/db
func eachBackend(t *testing.T, test func(t *testing.T, d DB)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) { test(t, NewMemory()) })
	if endpoint := os.Getenv("LAUNCHPAD_DYNAMODB_ENDPOINT"); endpoint != "" {
		t.Run("dynamodb", func(t *testing.T) { test(t, newTable(t, endpoint)) })
	}
}

// newTable creates a table shaped like the one in template.yaml.
func newTable(t *testing.T, endpoint string) DB {
	t.Helper()
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	client := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(endpoint) })
	name := fmt.Sprintf("launchpad-test-%d", time.Now().UnixNano())
	_, err = client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   &name,
		BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange},
		},
	})
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { _, _ = client.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: &name}) })
	return NewDynamo(client, name)
}

func TestPutGetRoundTripKeepsNumbers(t *testing.T) {
	eachBackend(t, func(t *testing.T, d DB) {
		if err := PutAs(ctx, d, row{PK: "A", SK: "B", N: 5}, nil); err != nil {
			t.Fatal(err)
		}
		got, err := GetAs[row](ctx, d, "A", "B")
		if err != nil || got == nil || got.N != 5 {
			t.Fatalf("got %+v, %v", got, err)
		}
		if missing, err := GetAs[row](ctx, d, "A", "nope"); missing != nil || err != nil {
			t.Fatalf("a missing item came back: %+v, %v", missing, err)
		}
	})
}

func TestPutConditionBlocksOverwrite(t *testing.T) {
	eachBackend(t, func(t *testing.T, d DB) {
		_ = PutAs(ctx, d, row{PK: "A", SK: "B"}, nil)
		err := PutAs(ctx, d, row{PK: "A", SK: "B"}, Absent("pk"))
		if !errors.Is(err, ErrConditionFailed) {
			t.Fatalf("got %v, want ErrConditionFailed", err)
		}
		if err := PutAs(ctx, d, row{PK: "A", SK: "C"}, Absent("pk")); err != nil {
			t.Fatalf("a new item must be accepted: %v", err)
		}
	})
}

func TestUpdateAddsUnderACondition(t *testing.T) {
	eachBackend(t, func(t *testing.T, d DB) {
		_ = PutAs(ctx, d, row{PK: "A", SK: "B", N: 1}, nil)
		add := Update{Add: map[string]int64{"n": 1}}
		got, err := UpdateAs[row](ctx, d, "A", "B", add, Less("n", 2))
		if err != nil || got.N != 2 {
			t.Fatalf("got %+v, %v", got, err)
		}
		if _, err := UpdateAs[row](ctx, d, "A", "B", add, Less("n", 2)); !errors.Is(err, ErrConditionFailed) {
			t.Fatalf("got %v, want ErrConditionFailed", err)
		}
	})
}

func TestUpdateOfAMissingItemFailsItsCondition(t *testing.T) {
	eachBackend(t, func(t *testing.T, d DB) {
		_, err := d.Update(ctx, "A", "B", Update{Set: map[string]any{"n": 1}}, Equal("n", 0))
		if !errors.Is(err, ErrConditionFailed) {
			t.Fatalf("got %v, want ErrConditionFailed", err)
		}
		_, err = d.Update(ctx, "A", "B", Update{Add: map[string]int64{"attempts": 1}}, Less("attempts", 5))
		if !errors.Is(err, ErrConditionFailed) {
			t.Fatalf("an add to an item that is not there: got %v, want ErrConditionFailed", err)
		}
		if got, _ := d.Get(ctx, "A", "B"); got != nil {
			t.Fatal("a failed update created the item")
		}
		item, err := d.Update(ctx, "A", "B", Update{Set: map[string]any{"n": 1}}, nil)
		if err != nil || item == nil {
			t.Fatalf("an unconditional update creates the item: %v", err)
		}
		if got, _ := GetAs[row](ctx, d, "A", "B"); got == nil || got.N != 1 {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestConditions(t *testing.T) {
	item := Item{
		"pk": &types.AttributeValueMemberS{Value: "A"}, "sk": &types.AttributeValueMemberS{Value: "B"},
		"status": &types.AttributeValueMemberS{Value: "running"}, "n": &types.AttributeValueMemberN{Value: "3"},
	}
	cases := []struct {
		name string
		cond Cond
		want bool
	}{
		{"absent attribute", Absent("nope"), true},
		{"present attribute", Absent("status"), false},
		{"equal", Equal("status", "running"), true},
		{"not equal", Equal("status", "failed"), false},
		{"equal on a missing attribute", Equal("nope", "x"), false},
		{"less", Less("n", 4), true},
		{"not less", Less("n", 3), false},
		{"less on a missing attribute", Less("nope", 4), false},
		{"one of", OneOf("status", "a", "running"), true},
		{"none of", OneOf("status", "a", "b"), false},
		{"and", And(Equal("status", "running"), Less("n", 4)), true},
		{"and with a failure", And(Equal("status", "running"), Less("n", 3)), false},
		{"or", Or(Absent("status"), Equal("status", "running")), true},
		{"or with none", Or(Absent("status"), Equal("status", "failed")), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := holds(c.cond, item)
			if err != nil || got != c.want {
				t.Fatalf("got %v, %v; want %v", got, err, c.want)
			}
		})
	}
}

func TestDeleteCondition(t *testing.T) {
	eachBackend(t, func(t *testing.T, d DB) {
		_ = PutAs(ctx, d, row{PK: "A", SK: "B", S: "v1"}, nil)
		if err := d.Delete(ctx, "A", "B", Equal("s", "v2")); !errors.Is(err, ErrConditionFailed) {
			t.Fatalf("got %v, want ErrConditionFailed", err)
		}
		if err := d.Delete(ctx, "A", "B", Equal("s", "v1")); err != nil {
			t.Fatal(err)
		}
		if got, _ := d.Get(ctx, "A", "B"); got != nil {
			t.Fatal("the item is still there")
		}
		if err := d.Delete(ctx, "A", "B", nil); err != nil {
			t.Fatalf("deleting what is not there: %v", err)
		}
	})
}

func TestQueryByPrefixInSortOrder(t *testing.T) {
	eachBackend(t, func(t *testing.T, d DB) {
		for _, sk := range []string{"DEMO#b", "META", "DEMO#a"} {
			_ = PutAs(ctx, d, row{PK: "P", SK: sk}, nil)
		}
		_ = PutAs(ctx, d, row{PK: "Q", SK: "DEMO#z"}, nil)
		rows, err := QueryAs[row](ctx, d, "P", "DEMO#")
		if err != nil || len(rows) != 2 || rows[0].SK != "DEMO#a" || rows[1].SK != "DEMO#b" {
			t.Fatalf("got %+v, %v", rows, err)
		}
		all, _ := QueryAs[row](ctx, d, "P", "")
		if len(all) != 3 {
			t.Fatalf("got %d rows, want 3", len(all))
		}
	})
}

// DynamoDB returns at most 1 MB a page, so a big partition takes several calls.
func TestQueryReadsEveryPage(t *testing.T) {
	eachBackend(t, func(t *testing.T, d DB) {
		big := strings.Repeat("x", 100_000)
		for i := range 25 {
			_ = PutAs(ctx, d, row{PK: "P", SK: fmt.Sprintf("DEMO#%02d", i), S: big}, nil)
		}
		rows, err := QueryAs[row](ctx, d, "P", "DEMO#")
		if err != nil || len(rows) != 25 {
			t.Fatalf("got %d rows, %v", len(rows), err)
		}
		if rows[0].SK != "DEMO#00" || rows[24].SK != "DEMO#24" {
			t.Fatalf("not in sort order: %s ... %s", rows[0].SK, rows[24].SK)
		}
	})
}

// run is shaped like the item runs.State writes, with the attribute names that
// DynamoDB reserves ("status", "action", "error", "url", "ttl"), the empty
// strings the code stores, and a binary attribute.
type run struct {
	PK      string `dynamodbav:"pk"`
	SK      string `dynamodbav:"sk"`
	Status  string `dynamodbav:"status"`
	Action  string `dynamodbav:"action"`
	RunID   string `dynamodbav:"runId"`
	URL     string `dynamodbav:"url,omitempty"`
	Error   string `dynamodbav:"error,omitempty"`
	TTL     int64  `dynamodbav:"ttl"`
	Blob    []byte `dynamodbav:"blob,omitempty"`
	Resumes int64  `dynamodbav:"resumes"`
}

func TestTheConditionsAndUpdatesTheRunsUse(t *testing.T) {
	eachBackend(t, func(t *testing.T, d DB) {
		launch := run{PK: "DEMO#x", SK: "STATE", Status: "launching", Action: "apply", RunID: "r1"}
		mayLaunch := Or(Absent("pk"), Equal("status", "launch_failed"))
		if err := PutAs(ctx, d, launch, mayLaunch); err != nil {
			t.Fatalf("a launch of a demo that is not deployed: %v", err)
		}
		if err := PutAs(ctx, d, launch, mayLaunch); !errors.Is(err, ErrConditionFailed) {
			t.Fatalf("a second launch: got %v, want ErrConditionFailed", err)
		}
		if _, err := d.Update(ctx, "DEMO#x", "STATE", Update{Set: map[string]any{"status": "launch_failed", "error": "boom", "url": ""}}, Equal("runId", "r1")); err != nil {
			t.Fatal(err)
		}
		if err := PutAs(ctx, d, run{PK: "DEMO#x", SK: "STATE", Status: "launching", RunID: "r2"}, mayLaunch); err != nil {
			t.Fatalf("a retry of a failed launch: %v", err)
		}

		_, _ = d.Update(ctx, "DEMO#x", "STATE", Update{Set: map[string]any{"status": "running"}}, Equal("runId", "r2"))
		teardown := func(old, previous string) error {
			_, err := d.Update(ctx, "DEMO#x", "STATE", Update{Set: map[string]any{
				"status": "tearing_down", "action": "destroy", "runId": "r3", "resumes": 0, "ttl": 5, "error": "",
			}}, And(Equal("runId", old), Equal("status", previous), OneOf("status", "running", "launch_failed", "teardown_failed")))
			return err
		}
		if err := teardown("stale", "running"); !errors.Is(err, ErrConditionFailed) {
			t.Fatalf("a teardown on a stale run: got %v, want ErrConditionFailed", err)
		}
		if err := teardown("r2", "launching"); !errors.Is(err, ErrConditionFailed) {
			t.Fatalf("a teardown on the wrong status: got %v, want ErrConditionFailed", err)
		}
		if err := teardown("r2", "running"); err != nil {
			t.Fatal(err)
		}
		if err := teardown("r3", "tearing_down"); !errors.Is(err, ErrConditionFailed) {
			t.Fatalf("a teardown while tearing down: got %v, want ErrConditionFailed", err)
		}
		got, err := GetAs[run](ctx, d, "DEMO#x", "STATE")
		if err != nil || got.Status != "tearing_down" || got.Action != "destroy" || got.RunID != "r3" || got.Error != "" {
			t.Fatalf("got %+v, %v", got, err)
		}
		if err := d.Delete(ctx, "DEMO#x", "STATE", Equal("runId", "r2")); !errors.Is(err, ErrConditionFailed) {
			t.Fatalf("a delete by a stale run: got %v, want ErrConditionFailed", err)
		}
		if err := d.Delete(ctx, "DEMO#x", "STATE", Equal("runId", "r3")); err != nil {
			t.Fatal(err)
		}
	})
}

func TestBinaryAttributesAndLongTextRoundTrip(t *testing.T) {
	eachBackend(t, func(t *testing.T, d DB) {
		blob := []byte{0, 1, 2, 255, 254}
		tail := strings.Repeat("line of output\n", 2000)
		_ = PutAs(ctx, d, run{PK: "A", SK: "B", Blob: blob, Error: tail}, nil)
		got, err := GetAs[run](ctx, d, "A", "B")
		if err != nil || string(got.Blob) != string(blob) || got.Error != tail {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
}

func TestTheOneTimeCodeCounterCannotBeBeatenByParallelGuesses(t *testing.T) {
	eachBackend(t, func(t *testing.T, d DB) {
		_ = PutAs(ctx, d, row{PK: "OTP#owner", SK: "OTP", N: 0}, nil)
		results := make(chan error, 20)
		for range 20 {
			go func() {
				_, err := d.Update(ctx, "OTP#owner", "OTP", Update{Add: map[string]int64{"n": 1}}, Less("n", 5))
				results <- err
			}()
		}
		counted := 0
		for range 20 {
			if err := <-results; err == nil {
				counted++
			} else if !errors.Is(err, ErrConditionFailed) {
				t.Fatal(err)
			}
		}
		if got, _ := GetAs[row](ctx, d, "OTP#owner", "OTP"); counted != 5 || got.N != 5 {
			t.Fatalf("%d guesses counted, counter %d; want 5 and 5", counted, got.N)
		}
	})
}

// fakeAPI records the requests the DynamoDB implementation makes.
type fakeAPI struct {
	put    *dynamodb.PutItemInput
	update *dynamodb.UpdateItemInput
	del    *dynamodb.DeleteItemInput
	query  []*dynamodb.QueryInput
	pages  []*dynamodb.QueryOutput
	err    error
}

func (f *fakeAPI) GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{}, f.err
}

func (f *fakeAPI) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.put = in
	return &dynamodb.PutItemOutput{}, f.err
}

func (f *fakeAPI) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	f.update = in
	return &dynamodb.UpdateItemOutput{Attributes: Key("A", "B")}, f.err
}

func (f *fakeAPI) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	f.del = in
	return &dynamodb.DeleteItemOutput{}, f.err
}

func (f *fakeAPI) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.query = append(f.query, in)
	page := f.pages[len(f.query)-1]
	return page, f.err
}

func TestDynamoTranslatesTheConditionsTheCodeUses(t *testing.T) {
	api := &fakeAPI{}
	d := NewDynamo(api, "launchpad")
	item, _ := Marshal(row{PK: "A", SK: "B"})

	if err := d.Put(ctx, item, Or(Absent("pk"), Equal("status", "launch_failed"))); err != nil {
		t.Fatal(err)
	}
	expr := *api.put.ConditionExpression
	for _, want := range []string{"attribute_not_exists", " OR "} {
		if !strings.Contains(expr, want) {
			t.Errorf("condition %q lacks %q", expr, want)
		}
	}
	// "status" is a reserved word, so it must have been given a name placeholder.
	if strings.Contains(expr, "status") || api.put.ExpressionAttributeNames == nil {
		t.Errorf("a reserved word is used as it is: %q %v", expr, api.put.ExpressionAttributeNames)
	}

	cond := And(Equal("runId", "r1"), Equal("status", "running"), OneOf("status", "running", "launch_failed"))
	if _, err := d.Update(ctx, "A", "B", Update{Set: map[string]any{"status": "tearing_down", "error": ""}, Add: map[string]int64{"attempts": 1}}, cond); err != nil {
		t.Fatal(err)
	}
	if got := *api.update.UpdateExpression; !strings.HasPrefix(got, "SET ") || strings.Count(got, ",") != 2 || !strings.Contains(got, "+") {
		t.Errorf("update expression %q", got)
	}
	if !strings.Contains(*api.update.ConditionExpression, " IN ") || api.update.ReturnValues != types.ReturnValueAllNew {
		t.Errorf("update request %+v", api.update)
	}

	if err := d.Delete(ctx, "A", "B", Equal("runId", "r1")); err != nil || api.del.ConditionExpression == nil {
		t.Fatalf("delete request %+v, %v", api.del, err)
	}
	if err := d.Delete(ctx, "A", "B", nil); err != nil || api.del.ConditionExpression != nil {
		t.Fatalf("an unconditional delete has a condition: %+v, %v", api.del, err)
	}
}

func TestDynamoMapsAFailedConditionAndNothingElse(t *testing.T) {
	api := &fakeAPI{err: &types.ConditionalCheckFailedException{}}
	d := NewDynamo(api, "launchpad")
	item, _ := Marshal(row{PK: "A", SK: "B"})
	if err := d.Put(ctx, item, Absent("pk")); !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("got %v, want ErrConditionFailed", err)
	}
	api.err = errors.New("throttled")
	err := d.Put(ctx, item, Absent("pk"))
	if err == nil || errors.Is(err, ErrConditionFailed) {
		t.Fatalf("got %v, want the original error", err)
	}
}

func TestDynamoQueryFollowsPages(t *testing.T) {
	api := &fakeAPI{pages: []*dynamodb.QueryOutput{
		{Items: []Item{Key("P", "DEMO#a")}, LastEvaluatedKey: Key("P", "DEMO#a")},
		{Items: []Item{Key("P", "DEMO#b")}},
	}}
	items, err := NewDynamo(api, "launchpad").Query(ctx, "P", "DEMO#")
	if err != nil || len(items) != 2 || len(api.query) != 2 {
		t.Fatalf("got %d items, %d calls, %v", len(items), len(api.query), err)
	}
	if api.query[1].ExclusiveStartKey == nil || !*api.query[0].ConsistentRead {
		t.Errorf("paging or consistency is wrong: %+v", api.query)
	}
	if !strings.Contains(*api.query[0].KeyConditionExpression, "begins_with") {
		t.Errorf("no prefix in %q", *api.query[0].KeyConditionExpression)
	}
}
