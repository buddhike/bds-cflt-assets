package server

import (
	"context"

	"apidiag/internal/health"
	"apidiag/internal/reports"
	"apidiag/internal/simulator"
)

// The topics the Flink statements in tf/flink write to.
const (
	UnhealthyEventsTopic = "api_unhealthy_events"
	AnomalyReportsTopic  = "api_anomaly_ai_reports"
)

// NewDeps connects the service to the Confluent Cloud cluster Terraform made.
func NewDeps(cfg Config) Deps {
	return Deps{
		NewSink: func() (simulator.Sink, func(), error) {
			p, err := simulator.NewProducer(context.Background(), cfg.KafkaBootstrap, cfg.KafkaAPIKey, cfg.KafkaAPISecret, cfg.Topic,
				cfg.SchemaRegistryURL, cfg.SchemaRegistryAPIKey, cfg.SchemaRegistryAPISecret)
			if err != nil {
				return nil, nil, err
			}
			return p, p.Close, nil
		},
		RunHealth: func(ctx context.Context, onEvent func(health.HealthEvent), onErr func(error)) error {
			c, err := health.NewConsumer(cfg.KafkaBootstrap, cfg.KafkaAPIKey, cfg.KafkaAPISecret, UnhealthyEventsTopic,
				cfg.SchemaRegistryURL, cfg.SchemaRegistryAPIKey, cfg.SchemaRegistryAPISecret)
			if err != nil {
				return err
			}
			defer c.Close()
			c.Run(ctx, onEvent, onErr)
			return nil
		},
		RunReports: func(ctx context.Context, onReport func(reports.Report), onErr func(error)) error {
			c, err := reports.NewConsumer(cfg.KafkaBootstrap, cfg.KafkaAPIKey, cfg.KafkaAPISecret, AnomalyReportsTopic,
				cfg.SchemaRegistryURL, cfg.SchemaRegistryAPIKey, cfg.SchemaRegistryAPISecret)
			if err != nil {
				return err
			}
			defer c.Close()
			c.Run(ctx, onReport, onErr)
			return nil
		},
	}
}
