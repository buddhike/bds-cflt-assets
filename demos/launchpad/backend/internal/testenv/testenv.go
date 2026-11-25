// Package testenv builds Services for tests: an in-memory table, fake AWS
// clients that record what they are asked, a fake GitHub and a clock that a
// test can move.
package testenv

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	"launchpad/internal/config"
	"launchpad/internal/db"
	"launchpad/internal/metrics"
	"launchpad/internal/services"
)

const (
	OwnerPhone = "+61400000001"
	OwnerEmail = "boss@confluent.io"
	SecretID   = "arn:aws:secretsmanager:us-east-1:123456789012:secret:launchpad-sign-in"
	KeyID      = "key-1"
	StartTime  = 1_700_000_000
)

// Env is a Services and the fakes behind it.
type Env struct {
	*services.Services
	// Table is the DB the services use. It is in memory, or a real DynamoDB table
	// when LAUNCHPAD_DYNAMODB_ENDPOINT is set (see NewTable).
	Table   db.DB
	SNS     *SNS
	KMS     *KMS
	Secrets *Secrets
	Lambda  *Lambda
	GitHub  *GitHub

	clock   int64
	mu      sync.Mutex
	logs    bytes.Buffer
	emitted bytes.Buffer
}

// New returns an Env whose clock is at StartTime and whose sign-in secret holds OwnerPhone.
func New(t testing.TB) *Env {
	t.Helper()
	e := &Env{
		Table:   NewTable(t),
		SNS:     &SNS{},
		KMS:     &KMS{Key: KeyID},
		Secrets: &Secrets{values: map[string]string{SecretID: OwnerPhone}},
		Lambda:  &Lambda{},
		GitHub:  NewGitHub(),
		clock:   StartTime,
	}
	e.Services = &services.Services{
		Settings: config.Settings{
			TableName: "launchpad", KMSKeyID: KeyID, StateBucket: "state-bucket", Region: "us-east-1",
			SuperUserEmail: OwnerEmail, DemosRepo: "owner/repo", DemosPath: "demos", DemosRef: "main",
			RunnerFunction: "launchpad-runner", SignInSecret: SecretID, SessionHours: 12,
		},
		DB:      e.Table,
		KMS:     e.KMS,
		SNS:     e.SNS,
		Secrets: e.Secrets,
		Lambda:  e.Lambda,
		HTTP:    &http.Client{Transport: e.GitHub},
		Metrics: &metrics.Emitter{Out: lockedWriter{e, &e.emitted}, Now: func() time.Time { return time.Unix(e.Now(), 0) }},
		Log:     slog.New(slog.NewJSONHandler(lockedWriter{e, &e.logs}, nil)),
		Now:     e.Now,
	}
	return e
}

// Now is the test clock, in seconds since the epoch.
func (e *Env) Now() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.clock
}

// Advance moves the clock forward.
func (e *Env) Advance(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.clock += int64(d / time.Second)
}

// Logs is everything logged so far.
func (e *Env) Logs() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.logs.String()
}

// Emitted is every metric line written so far.
func (e *Env) Emitted() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.emitted.String()
}

type lockedWriter struct {
	e *Env
	b *bytes.Buffer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.e.mu.Lock()
	defer w.e.mu.Unlock()
	return w.b.Write(p)
}

// --- SNS -------------------------------------------------------------------

// Text is one text message that was sent.
type Text struct {
	Number  string
	Message string
	Input   *sns.PublishInput
}

var codePattern = regexp.MustCompile(`code: (\d{6})\.`)

// Code is the six digit code in the message.
func (t Text) Code() string {
	if m := codePattern.FindStringSubmatch(t.Message); m != nil {
		return m[1]
	}
	return ""
}

// SNS records published text messages.
type SNS struct {
	mu   sync.Mutex
	sent []Text
	Err  error // returned by every Publish when set
}

func (s *SNS) Publish(_ context.Context, in *sns.PublishInput, _ ...func(*sns.Options)) (*sns.PublishOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return nil, s.Err
	}
	number, message := "", ""
	if in.PhoneNumber != nil {
		number = *in.PhoneNumber
	}
	if in.Message != nil {
		message = *in.Message
	}
	s.sent = append(s.sent, Text{Number: number, Message: message, Input: in})
	id := "message-1"
	return &sns.PublishOutput{MessageId: &id}, nil
}

// Sent is every text message published so far.
func (s *SNS) Sent() []Text {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Text(nil), s.sent...)
}

// LastCode is the code in the latest message, or "".
func (s *SNS) LastCode() string {
	sent := s.Sent()
	if len(sent) == 0 {
		return ""
	}
	return sent[len(sent)-1].Code()
}

// --- Secrets Manager -------------------------------------------------------

// Secrets holds secret values by id.
type Secrets struct {
	mu     sync.Mutex
	values map[string]string
}

// Put sets the value of a secret.
func (s *Secrets) Put(id, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[id] = value
}

func (s *Secrets) GetSecretValue(_ context.Context, in *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := ""
	if in.SecretId != nil {
		id = *in.SecretId
	}
	value, ok := s.values[id]
	if !ok {
		return nil, &smtypes.ResourceNotFoundException{Message: strPtr("Secrets Manager can't find the specified secret.")}
	}
	return &secretsmanager.GetSecretValueOutput{SecretString: &value}, nil
}

// --- KMS -------------------------------------------------------------------

// KMS encrypts reversibly and binds the ciphertext to its key and context, as
// the real one does. The ciphertext does not contain the plaintext.
type KMS struct {
	Key string
}

func (k *KMS) Encrypt(_ context.Context, in *kms.EncryptInput, _ ...func(*kms.Options)) (*kms.EncryptOutput, error) {
	if in.KeyId == nil || *in.KeyId != k.Key {
		return nil, errors.New("NotFoundException: no such key")
	}
	mask := maskFor(in.EncryptionContext)
	sealed := make([]byte, len(in.Plaintext))
	for i, b := range in.Plaintext {
		sealed[i] = b ^ mask[i%len(mask)]
	}
	blob := hex.EncodeToString(sealed) + "|" + contextString(in.EncryptionContext)
	return &kms.EncryptOutput{CiphertextBlob: []byte(blob)}, nil
}

func (k *KMS) Decrypt(_ context.Context, in *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	if in.KeyId == nil || *in.KeyId != k.Key {
		return nil, errors.New("IncorrectKeyException")
	}
	sealedHex, ctx, ok := strings.Cut(string(in.CiphertextBlob), "|")
	if !ok || ctx != contextString(in.EncryptionContext) {
		return nil, errors.New("InvalidCiphertextException: the encryption context does not match")
	}
	sealed, err := hex.DecodeString(sealedHex)
	if err != nil {
		return nil, errors.New("InvalidCiphertextException")
	}
	mask := maskFor(in.EncryptionContext)
	plain := make([]byte, len(sealed))
	for i, b := range sealed {
		plain[i] = b ^ mask[i%len(mask)]
	}
	return &kms.DecryptOutput{Plaintext: plain}, nil
}

func contextString(c map[string]string) string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + c[k]
	}
	return strings.Join(parts, ",")
}

func maskFor(c map[string]string) []byte {
	mask := []byte("k3y" + contextString(c))
	return mask
}

// --- Lambda ----------------------------------------------------------------

// Invocation is one call of Lambda's Invoke.
type Invocation struct {
	Function string
	Type     string
	Payload  map[string]any
}

// Lambda records invocations.
type Lambda struct {
	mu    sync.Mutex
	calls []Invocation
	Err   error // returned by every Invoke when set
}

func (l *Lambda) Invoke(_ context.Context, in *lambda.InvokeInput, _ ...func(*lambda.Options)) (*lambda.InvokeOutput, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Err != nil {
		return nil, l.Err
	}
	var payload map[string]any
	if err := json.Unmarshal(in.Payload, &payload); err != nil {
		return nil, fmt.Errorf("payload is not a JSON object: %w", err)
	}
	fn := ""
	if in.FunctionName != nil {
		fn = *in.FunctionName
	}
	l.calls = append(l.calls, Invocation{Function: fn, Type: string(in.InvocationType), Payload: payload})
	return &lambda.InvokeOutput{StatusCode: 202}, nil
}

// Calls is every invocation so far.
func (l *Lambda) Calls() []Invocation {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Invocation(nil), l.calls...)
}

// Clear forgets the invocations so far.
func (l *Lambda) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = nil
}

func strPtr(s string) *string { return &s }

// NewTable is the table the tests keep their items in. It is an in-memory DB,
// unless LAUNCHPAD_DYNAMODB_ENDPOINT names a DynamoDB endpoint, such as
// DynamoDB Local. Then every test gets a table of its own there, with the keys
// of the table in template.yaml, and runs against the real service:
//
//	docker run -d --rm -p 8000:8000 amazon/dynamodb-local -jar DynamoDBLocal.jar -inMemory
//	LAUNCHPAD_DYNAMODB_ENDPOINT=http://localhost:8000 go test ./...
func NewTable(t testing.TB) db.DB {
	t.Helper()
	endpoint := os.Getenv("LAUNCHPAD_DYNAMODB_ENDPOINT")
	if endpoint == "" {
		return db.NewMemory()
	}
	ctx := context.Background()
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	client := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(endpoint) })
	name := fmt.Sprintf("launchpad-test-%d", time.Now().UnixNano())
	_, err = client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   &name,
		BillingMode: ddbtypes.BillingModePayPerRequest,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: ddbtypes.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: ddbtypes.KeyTypeRange},
		},
	})
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { _, _ = client.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: &name}) })
	return db.NewDynamo(client, name)
}
