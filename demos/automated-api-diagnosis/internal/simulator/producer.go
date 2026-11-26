package simulator

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

// Producer is a thin wrapper around a franz-go client bound to one topic,
// encoding values with confluent-kafka-go's official Avro Schema Registry
// serializer (which also handles schema registration/lookup and the
// Confluent wire format - magic byte + schema ID).
type Producer struct {
	client     *kgo.Client
	topic      string
	serializer *avro.GenericSerializer
}

// NewProducer connects to a Confluent Cloud Kafka cluster over SASL_SSL (via
// franz-go) and to its paired Schema Registry (via confluent-kafka-go's
// schemaregistry client), building an Avro serializer for KongLogEvent.
//
// bootstrap may include a scheme prefix (e.g. "SASL_SSL://host:9092", as
// returned by the confluent_kafka_cluster Terraform resource's
// bootstrap_endpoint output) - it is stripped automatically.
func NewProducer(ctx context.Context, bootstrap, apiKey, apiSecret, topic, srURL, srAPIKey, srAPISecret string) (*Producer, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(normalizeBootstrap(bootstrap)),
		kgo.SASL(plain.Auth{User: apiKey, Pass: apiSecret}.AsMechanism()),
		kgo.DialTLSConfig(&tls.Config{}),
	)
	if err != nil {
		return nil, err
	}

	srClient, err := schemaregistry.NewClient(schemaregistry.NewConfigWithBasicAuthentication(srURL, srAPIKey, srAPISecret))
	if err != nil {
		client.Close()
		return nil, err
	}

	serializer, err := avro.NewGenericSerializer(srClient, serde.ValueSerde, avro.NewSerializerConfig())
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("building avro serializer: %w", err)
	}

	return &Producer{client: client, topic: topic, serializer: serializer}, nil
}

// Produce asynchronously Avro-encodes ev and sends it to the topic, keyed by
// key so all events for the same API land on the same partition. onErr (if
// non-nil) is called with any encode or delivery error.
func (p *Producer) Produce(ctx context.Context, key string, ev KongLogEvent, onErr func(error)) {
	value, err := p.serializer.Serialize(p.topic, &ev)
	if err != nil {
		if onErr != nil {
			onErr(fmt.Errorf("avro serialize: %w", err))
		}
		return
	}
	p.client.Produce(ctx, &kgo.Record{Topic: p.topic, Key: []byte(key), Value: value}, func(_ *kgo.Record, err error) {
		if err != nil && onErr != nil {
			onErr(err)
		}
	})
}

func (p *Producer) Close() {
	p.client.Close()
}

func normalizeBootstrap(s string) string {
	if idx := strings.Index(s, "://"); idx != -1 {
		return s[idx+3:]
	}
	return s
}
