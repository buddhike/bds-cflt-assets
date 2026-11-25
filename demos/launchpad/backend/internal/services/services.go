// Package services holds the AWS clients and settings the code needs, built once per container.
package services

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	appconfig "launchpad/internal/config"
	"launchpad/internal/db"
	"launchpad/internal/metrics"
)

// KMS is the part of the KMS client the code uses.
type KMS interface {
	Encrypt(ctx context.Context, in *kms.EncryptInput, opts ...func(*kms.Options)) (*kms.EncryptOutput, error)
	Decrypt(ctx context.Context, in *kms.DecryptInput, opts ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

// SNS is the part of the SNS client the code uses.
type SNS interface {
	Publish(ctx context.Context, in *sns.PublishInput, opts ...func(*sns.Options)) (*sns.PublishOutput, error)
}

// Secrets is the part of the Secrets Manager client the code uses.
type Secrets interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// Lambda is the part of the Lambda client the code uses.
type Lambda interface {
	Invoke(ctx context.Context, in *lambda.InvokeInput, opts ...func(*lambda.Options)) (*lambda.InvokeOutput, error)
}

// HTTP sends outbound requests. *http.Client is one. Tests replace it.
type HTTP interface {
	Do(req *http.Request) (*http.Response, error)
}

// Services is everything a request needs.
type Services struct {
	Settings appconfig.Settings
	DB       db.DB
	KMS      KMS
	SNS      SNS
	Secrets  Secrets
	Lambda   Lambda
	HTTP     HTTP
	Metrics  *metrics.Emitter
	Log      *slog.Logger
	// Now is the time in seconds since the epoch. Tests move it.
	Now func() int64
}

// FromEnv builds the services for a Lambda function from its environment.
func FromEnv(ctx context.Context) (*Services, error) {
	settings, err := appconfig.FromEnv(os.Getenv)
	if err != nil {
		return nil, err
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(settings.Region))
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	return &Services{
		Settings: settings,
		DB:       db.NewDynamo(dynamodb.NewFromConfig(cfg), settings.TableName),
		KMS:      kms.NewFromConfig(cfg),
		SNS:      sns.NewFromConfig(cfg),
		Secrets:  secretsmanager.NewFromConfig(cfg),
		Lambda:   lambda.NewFromConfig(cfg),
		HTTP:     &http.Client{},
		Metrics:  &metrics.Emitter{Out: os.Stdout, Now: time.Now},
		Log:      slog.New(slog.NewJSONHandler(os.Stdout, nil)),
		Now:      func() int64 { return time.Now().Unix() },
	}, nil
}
