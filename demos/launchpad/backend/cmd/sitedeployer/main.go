// Command sitedeployer is the Lambda function behind the SiteContent custom resource.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"launchpad/internal/site"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Error("Could not load the AWS configuration", "error", err)
		os.Exit(1)
	}
	d := &site.Deployer{
		S3:         s3.NewFromConfig(cfg),
		CloudFront: cloudfront.NewFromConfig(cfg),
		HTTP:       &http.Client{},
		Log:        log,
		Now:        time.Now,
	}
	lambda.Start(d.Handle)
}
