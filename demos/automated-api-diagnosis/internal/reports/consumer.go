package reports

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/confluentinc/confluent-kafka-go/v2/schemaregistry"
	"github.com/confluentinc/confluent-kafka-go/v2/schemaregistry/serde"
	"github.com/confluentinc/confluent-kafka-go/v2/schemaregistry/serde/avro"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
)

// Consumer reads Report rows from a Kafka topic, decoding Avro wire-format
// values via confluent-kafka-go's Schema Registry deserializer - the same
// approach as internal/health.Consumer.
type Consumer struct {
	client          *kgo.Client
	valDeserializer *avro.GenericDeserializer
}

// NewConsumer connects to a Confluent Cloud Kafka cluster over SASL_SSL
// (via franz-go) and to its paired Schema Registry.
//
// It always starts from the tail of the topic - the AI REPORTS tab shows
// reports for anomalies detected while the app is open, not a replay of
// old ones. extra options are applied last and can override that default
// (the smoke test uses this to read a report that's already on the topic).
//
// bootstrap may include a scheme prefix (e.g. "SASL_SSL://host:9092") -
// it is stripped automatically, same as internal/health.NewConsumer.
func NewConsumer(bootstrap, apiKey, apiSecret, topic, srURL, srAPIKey, srAPISecret string, extra ...kgo.Opt) (*Consumer, error) {
	opts := append([]kgo.Opt{
		kgo.SeedBrokers(normalizeBootstrap(bootstrap)),
		kgo.SASL(plain.Auth{User: apiKey, Pass: apiSecret}.AsMechanism()),
		kgo.DialTLSConfig(&tls.Config{}),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtEnd()),
	}, extra...)
	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, err
	}

	srClient, err := schemaregistry.NewClient(schemaregistry.NewConfigWithBasicAuthentication(srURL, srAPIKey, srAPISecret))
	if err != nil {
		client.Close()
		return nil, err
	}

	valDeserializer, err := avro.NewGenericDeserializer(srClient, serde.ValueSerde, avro.NewDeserializerConfig())
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("building avro value deserializer: %w", err)
	}

	return &Consumer{client: client, valDeserializer: valDeserializer}, nil
}

// Run polls for records until ctx is done, decoding each into a Report
// (with <reasoning> blocks stripped) and invoking onReport. Decode and
// fetch errors are reported via onErr (if non-nil) and skipped, rather than
// aborting the loop.
func (c *Consumer) Run(ctx context.Context, onReport func(Report), onErr func(error)) {
	for {
		fetches := c.client.PollFetches(ctx)
		if ctx.Err() != nil {
			return
		}
		fetches.EachError(func(_ string, _ int32, err error) {
			if onErr != nil {
				onErr(err)
			}
		})
		fetches.EachRecord(func(r *kgo.Record) {
			var v row
			if err := c.valDeserializer.DeserializeInto(r.Topic, r.Value, &v); err != nil {
				if onErr != nil {
					onErr(fmt.Errorf("avro value deserialize: %w", err))
				}
				return
			}
			onReport(Report{
				ID:          fmt.Sprintf("%d-%d", r.Partition, r.Offset),
				APIID:       v.APIID,
				HTTPStatus:  v.HTTPStatus,
				DetectedAt:  v.DetectedAt,
				AgentStatus: v.AgentStatus,
				Report:      StripReasoning(v.Report),
			})
		})
	}
}

func (c *Consumer) Close() {
	c.client.Close()
}

func normalizeBootstrap(s string) string {
	if idx := strings.Index(s, "://"); idx != -1 {
		return s[idx+3:]
	}
	return s
}
