// Package credentials keeps each user's Confluent Cloud API key, encrypted.
//
// The key and secret are encrypted with KMS before they reach DynamoDB, bound to
// the user's email through the encryption context, so a copy of one user's blob
// under another user's name does not decrypt. Only the runner decrypts them.
// The API only ever says whether a key is saved and shows the last four
// characters of its id.
package credentials

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/service/kms"

	"launchpad/internal/apperr"
	"launchpad/internal/db"
	"launchpad/internal/services"
	"launchpad/internal/users"
)

const maxLength = 200

// Credentials is a decrypted API key and secret. It never prints the secret.
type Credentials struct {
	APIKey    string
	APISecret string
}

func (c Credentials) String() string {
	hint := c.APIKey
	if len(hint) > 4 {
		hint = hint[:4]
	}
	return fmt.Sprintf("Credentials(api_key=%s***, api_secret=***)", hint)
}

// GoString covers %#v.
func (c Credentials) GoString() string { return c.String() }

// LogValue keeps the secret out of structured logs.
func (c Credentials) LogValue() slog.Value { return slog.StringValue(c.String()) }

// Status is what the API says about a user's saved key.
type Status struct {
	Configured bool   `json:"configured"`
	APIKeyHint string `json:"apiKeyHint,omitempty"`
	UpdatedAt  int64  `json:"updatedAt,omitempty"`
}

type stored struct {
	PK         string `dynamodbav:"pk"`
	SK         string `dynamodbav:"sk"`
	Ciphertext []byte `dynamodbav:"ciphertext"`
	APIKeyHint string `dynamodbav:"apiKeyHint"`
	UpdatedAt  int64  `dynamodbav:"updatedAt"`
}

type plaintext struct {
	Key    string `json:"key"`
	Secret string `json:"secret"`
}

func encryptionContext(email string) map[string]string {
	return map[string]string{"launchpad:user": email}
}

func clean(name string, value any) (string, error) {
	s, ok := value.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", apperr.BadRequest("invalid_credentials", "Enter the "+name)
	}
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxLength || strings.IndexFunc(s, unicode.IsSpace) >= 0 {
		return "", apperr.BadRequest("invalid_credentials", "The "+name+" is not valid")
	}
	return s, nil
}

// Save encrypts and stores a user's key and secret.
func Save(ctx context.Context, svc *services.Services, email string, apiKey, apiSecret any) (Status, error) {
	key, err := clean("API key", apiKey)
	if err != nil {
		return Status{}, err
	}
	secret, err := clean("API secret", apiSecret)
	if err != nil {
		return Status{}, err
	}
	body, err := json.Marshal(plaintext{Key: key, Secret: secret})
	if err != nil {
		return Status{}, fmt.Errorf("encode credentials: %w", err)
	}
	enc, err := svc.KMS.Encrypt(ctx, &kms.EncryptInput{
		KeyId:             &svc.Settings.KMSKeyID,
		Plaintext:         body,
		EncryptionContext: encryptionContext(email),
	})
	if err != nil {
		return Status{}, fmt.Errorf("encrypt credentials: %w", err)
	}
	hint := key
	if r := []rune(key); len(r) > 4 {
		hint = string(r[len(r)-4:])
	}
	err = db.PutAs(ctx, svc.DB, stored{
		PK: users.PK(email), SK: "CREDS", Ciphertext: enc.CiphertextBlob, APIKeyHint: hint, UpdatedAt: svc.Now(),
	}, nil)
	if err != nil {
		return Status{}, err
	}
	svc.Metrics.Count("CredentialsSaved")
	return GetStatus(ctx, svc, email)
}

// GetStatus says whether a user has saved a key.
func GetStatus(ctx context.Context, svc *services.Services, email string) (Status, error) {
	item, err := db.GetAs[stored](ctx, svc.DB, users.PK(email), "CREDS")
	if err != nil {
		return Status{}, err
	}
	if item == nil {
		return Status{Configured: false}, nil
	}
	return Status{Configured: true, APIKeyHint: item.APIKeyHint, UpdatedAt: item.UpdatedAt}, nil
}

// Delete removes a user's saved key.
func Delete(ctx context.Context, svc *services.Services, email string) error {
	return svc.DB.Delete(ctx, users.PK(email), "CREDS", nil)
}

// Load decrypts a user's key, for the runner. It returns nil if none is saved.
func Load(ctx context.Context, svc *services.Services, email string) (*Credentials, error) {
	item, err := db.GetAs[stored](ctx, svc.DB, users.PK(email), "CREDS")
	if err != nil || item == nil {
		return nil, err
	}
	dec, err := svc.KMS.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob:    item.Ciphertext,
		KeyId:             &svc.Settings.KMSKeyID,
		EncryptionContext: encryptionContext(email),
	})
	if err != nil {
		return nil, fmt.Errorf("decrypt credentials: %w", err)
	}
	var p plaintext
	if err := json.Unmarshal(dec.Plaintext, &p); err != nil {
		return nil, fmt.Errorf("decode credentials: %w", err)
	}
	return &Credentials{APIKey: p.Key, APISecret: p.Secret}, nil
}
