package credentials_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"launchpad/internal/apperr"
	"launchpad/internal/credentials"
	"launchpad/internal/db"
	"launchpad/internal/testenv"
	"launchpad/internal/users"
)

var ctx = context.Background()

const ann = "ann@confluent.io"

func TestRoundTrip(t *testing.T) {
	env := testenv.New(t)
	status, err := credentials.Save(ctx, env.Services, ann, " KEY12345 ", "s3cret/+=")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Configured || status.APIKeyHint != "2345" || status.UpdatedAt != testenv.StartTime {
		t.Fatalf("status %+v", status)
	}
	got, err := credentials.Load(ctx, env.Services, ann)
	if err != nil || got == nil || got.APIKey != "KEY12345" || got.APISecret != "s3cret/+=" {
		t.Fatalf("loaded %+v, %v", got, err)
	}
}

func TestTheSecretIsNotStoredOrReturnedInClear(t *testing.T) {
	env := testenv.New(t)
	status, _ := credentials.Save(ctx, env.Services, ann, "KEY12345", "topsecretvalue")
	raw, _ := env.Table.Get(ctx, users.PK(ann), "CREDS")
	var stored struct {
		Ciphertext []byte `dynamodbav:"ciphertext"`
	}
	if err := db.Unmarshal(raw, &stored); err != nil || len(stored.Ciphertext) == 0 {
		t.Fatalf("no ciphertext: %v", err)
	}
	if bytes.Contains(stored.Ciphertext, []byte("topsecretvalue")) {
		t.Fatal("the secret is stored in clear")
	}
	if strings.Contains(fmt.Sprintf("%+v", status), "topsecretvalue") {
		t.Fatal("the status shows the secret")
	}
}

func TestTheBlobIsBoundToItsOwner(t *testing.T) {
	env := testenv.New(t)
	_, _ = credentials.Save(ctx, env.Services, ann, "KEY12345", "topsecretvalue")
	blob, _ := env.Table.Get(ctx, users.PK(ann), "CREDS")
	copied := db.Item{}
	for k, v := range blob {
		copied[k] = v
	}
	copied["pk"] = db.Key("USER#bob@confluent.io", "")["pk"]
	if err := env.Table.Put(ctx, copied, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := credentials.Load(ctx, env.Services, "bob@confluent.io"); err == nil {
		t.Fatalf("another user's blob decrypted: %+v", got)
	}
}

func TestStatusLoadAndDeleteWhenThereIsNone(t *testing.T) {
	env := testenv.New(t)
	if status, _ := credentials.GetStatus(ctx, env.Services, ann); status.Configured {
		t.Fatal("configured before anything was saved")
	}
	if got, err := credentials.Load(ctx, env.Services, ann); got != nil || err != nil {
		t.Fatalf("loaded %+v, %v", got, err)
	}
	_, _ = credentials.Save(ctx, env.Services, ann, "KEY12345", strings.Repeat("x", 10))
	if err := credentials.Delete(ctx, env.Services, ann); err != nil {
		t.Fatal(err)
	}
	if got, _ := credentials.Load(ctx, env.Services, ann); got != nil {
		t.Fatal("still there after Delete")
	}
}

func TestInvalidInputIsRejected(t *testing.T) {
	cases := []struct{ key, secret any }{
		{"", "x"}, {"k", ""}, {"k k", "x"}, {"k", "x\ny"}, {nil, "x"}, {"k", 3.0}, {strings.Repeat("k", 201), "x"},
	}
	for _, c := range cases {
		env := testenv.New(t)
		_, err := credentials.Save(ctx, env.Services, ann, c.key, c.secret)
		if appErr, ok := apperr.As(err); !ok || appErr.Status != 400 {
			t.Errorf("Save(%v, %v): %v", c.key, c.secret, err)
		}
		if status, _ := credentials.GetStatus(ctx, env.Services, ann); status.Configured {
			t.Errorf("Save(%v, %v) stored something", c.key, c.secret)
		}
	}
}

func TestACredentialNeverPrintsItsSecret(t *testing.T) {
	c := credentials.Credentials{APIKey: "ABCDEFG", APISecret: "hunter2"}
	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("loaded", "credentials", c)
	for name, text := range map[string]string{
		"%v": fmt.Sprintf("%v", c), "%+v": fmt.Sprintf("%+v", c), "%#v": fmt.Sprintf("%#v", c),
		"%v of a pointer": fmt.Sprintf("%v", &c), "slog": logged.String(),
	} {
		if strings.Contains(text, "hunter2") {
			t.Errorf("%s shows the secret: %s", name, text)
		}
	}
}
