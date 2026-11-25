// Command api is the Lambda function behind API Gateway (payload format 2.0).
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"

	"launchpad/internal/api"
	"launchpad/internal/apperr"
	"launchpad/internal/services"
)

func main() {
	svc, err := services.FromEnv(context.Background())
	if err != nil {
		slog.Error("Could not set up the API", "error", err)
		os.Exit(1)
	}
	lambda.Start(func(ctx context.Context, event events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
		req, err := api.FromEvent(event)
		if err != nil {
			appErr, _ := apperr.As(err)
			return api.ToEvent(api.Response{Status: appErr.Status, Body: map[string]any{
				"error": map[string]string{"code": appErr.Code, "message": appErr.Message},
			}}), nil
		}
		return api.ToEvent(api.Dispatch(ctx, svc, req)), nil
	})
}
