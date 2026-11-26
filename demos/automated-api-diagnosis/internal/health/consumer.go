package health

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

// Consumer reads HealthEvent rows from a Kafka topic, decoding Avro
// wire-format values via confluent-kafka-go's official Schema Registry
// deserializer - the same library internal/simulator.Producer uses for
// the reverse (producing) direction.
type Consumer struct {
	client          *kgo.Client
	keyDeserializer *avro.GenericDeserializer
	valDeserializer *avro.GenericDeserializer
}

// NewConsumer connects to a Confluent Cloud Kafka cluster over SASL_SSL
// (via franz-go) and to its paired Schema Registry, always starting from
// the tail of the topic - the MONITOR tab shows live status, not
// historical replay.
//
// bootstrap may include a scheme prefix (e.g. "SASL_SSL://host:9092") -
// it is stripped automatically, same as internal/simulator.NewProducer.
func NewConsumer(bootstrap, apiKey, apiSecret, topic, srURL, srAPIKey, srAPISecret string) (*Consumer, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(normalizeBootstrap(bootstrap)),
		kgo.SASL(plain.Auth{User: apiKey, Pass: apiSecret}.AsMechanism()),
		kgo.DialTLSConfig(&tls.Config{}),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtEnd()),
	)
	if err != nil {
		return nil, err
	}

	srClient, err := schemaregistry.NewClient(schemaregistry.NewConfigWithBasicAuthentication(srURL, srAPIKey, srAPISecret))
	if err != nil {
		client.Close()
		return nil, err
	}

	// The table's PRIMARY KEY (api_id, detected_at, http_status) makes
	// Flink's Kafka connector encode those columns in the message key,
	// not the value - the value schema only carries the remaining
	// (non-key) columns. So both need decoding into the same HealthEvent.
	keyDeserializer, err := avro.NewGenericDeserializer(srClient, serde.KeySerde, avro.NewDeserializerConfig())
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("building avro key deserializer: %w", err)
	}
	valDeserializer, err := avro.NewGenericDeserializer(srClient, serde.ValueSerde, avro.NewDeserializerConfig())
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("building avro value deserializer: %w", err)
	}

	return &Consumer{client: client, keyDeserializer: keyDeserializer, valDeserializer: valDeserializer}, nil
}

// Run polls for records until ctx is done, decoding each into a
// HealthEvent and invoking onEvent. Decode and fetch errors are reported
// via onErr (if non-nil) and skipped, rather than aborting the loop.
func (c *Consumer) Run(ctx context.Context, onEvent func(HealthEvent), onErr func(error)) {
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
			// Decoded into separate targets, not the same struct: heetch/avro
			// resets reader-side fields absent from whichever writer schema
			// it's resolving against (Avro schema-resolution's "apply the
			// reader's zero-value default" behavior), so decoding key then
			// value into one shared HealthEvent would have the value decode
			// wipe out the fields the key decode had just set.
			var keyEv, valEv HealthEvent
			if err := c.keyDeserializer.DeserializeInto(r.Topic, r.Key, &keyEv); err != nil {
				if onErr != nil {
					onErr(fmt.Errorf("avro key deserialize: %w", err))
				}
				return
			}
			if err := c.valDeserializer.DeserializeInto(r.Topic, r.Value, &valEv); err != nil {
				if onErr != nil {
					onErr(fmt.Errorf("avro value deserialize: %w", err))
				}
				return
			}
			ev := valEv
			ev.APIID = keyEv.APIID
			ev.DetectedAt = keyEv.DetectedAt
			ev.HTTPStatus = keyEv.HTTPStatus
			onEvent(ev)
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
