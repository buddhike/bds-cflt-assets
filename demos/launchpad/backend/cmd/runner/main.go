// Command runner is the Lambda function that runs Terraform. The API and the
// runner itself invoke it asynchronously.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"

	"launchpad/internal/runner"
	"launchpad/internal/services"
)

type response struct {
	Result string `json:"result"`
}

func main() {
	svc, err := services.FromEnv(context.Background())
	if err != nil {
		slog.Error("Could not set up the runner", "error", err)
		os.Exit(1)
	}
	work := os.Getenv("LAUNCHPAD_WORK_DIR")
	if work == "" {
		work = "/tmp/launchpad"
	}
	r := runner.New(svc, work)
	lambda.Start(func(ctx context.Context, ev runner.Event) (response, error) {
		remaining := func() time.Duration {
			if deadline, ok := ctx.Deadline(); ok {
				return time.Until(deadline)
			}
			return 0
		}
		result, err := r.Run(ctx, ev, remaining)
		return response{Result: result}, err
	})
}
