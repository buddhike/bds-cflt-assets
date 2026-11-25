package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// TableflowSignal tells the server whether Terraform has enabled Tableflow.
// Terraform cannot enable it until the server has made Debezium register the
// topic schemas, so the two have to agree on the order, and this is how the
// server finds out that its turn to wait is over.
type TableflowSignal interface {
	Enabled(ctx context.Context) (bool, error)
}

// SSMSignal reads an SSM parameter that Terraform sets to "true" once Tableflow is on.
type SSMSignal struct {
	client *ssm.Client
	name   string
}

// NewSSMSignal uses the task role's credentials.
func NewSSMSignal(ctx context.Context, region, name string) (*SSMSignal, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	return &SSMSignal{client: ssm.NewFromConfig(cfg), name: name}, nil
}

// Enabled is true once the parameter's value is "true".
func (s *SSMSignal) Enabled(ctx context.Context) (bool, error) {
	out, err := s.client.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(s.name)})
	if err != nil {
		var missing *ssmtypes.ParameterNotFound
		if errors.As(err, &missing) {
			return false, nil
		}
		return false, fmt.Errorf("read SSM parameter %s: %w", s.name, err)
	}
	return strings.EqualFold(aws.ToString(out.Parameter.Value), "true"), nil
}
