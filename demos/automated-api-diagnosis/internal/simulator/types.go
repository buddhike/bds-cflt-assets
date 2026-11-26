// Package simulator generates fake Kong-style API gateway log events for a
// fixed set of simulated APIs and produces them to a shared Kafka topic.
package simulator

import (
	"time"

	"github.com/heetch/avro/avrotypegen"
)

// APIConfig holds one simulated API's adjustable parameters.
type APIConfig struct {
	Name         string   `json:"name"`
	Path         string   `json:"path"`
	Rate         float64  `json:"rate"`   // events/sec
	Pct500       float64  `json:"pct500"` // 0-100
	Pct403       float64  `json:"pct403"` // 0-100
	Pct404       float64  `json:"pct404"` // 0-100
	LatencyMs    int      `json:"latencyMs"`
	GatewayError bool     `json:"gatewayError"`
	DependsOn    []string `json:"dependsOn"`
}

// APIStats is a live snapshot of one API's recent activity, broadcast to
// the frontend roughly twice a second while the simulation is running.
type APIStats struct {
	EventsPerSec   float64        `json:"eventsPerSec"`
	StatusCounts   map[string]int `json:"statusCounts"`
	LastDurationMs int            `json:"lastDurationMs"`
}

// Outcome is the result of the most recent simulated request for one API,
// published to the shared OutcomeBoard so dependent APIs can react to it.
type Outcome struct {
	Status     int
	DurationMs int
	Failed     bool
	At         time.Time
}

// KongLogEvent mirrors the shape of a real Kong API-gateway log line and is
// serialized with Avro via the confluent-kafka-go schema registry serde
// (see producer.go). Avro field names can't contain dots, so fields that
// were originally dotted (e.g. "request.method") use underscores instead
// (request_method).
//
// Timestamp is a plain int64 (Unix milliseconds), not time.Time: the
// underlying Avro codec (github.com/heetch/avro) has a hardcoded limitation
// that only lets a genuine time.Time field encode as timestamp-micros, never
// timestamp-millis (see AvroRecord below) - representing it as int64
// sidesteps that check entirely and lets us declare timestamp-millis
// (Flink's TIMESTAMP(3)) instead. Use TimestampTime() for a time.Time view.
type KongLogEvent struct {
	Timestamp        int64  `json:"timestamp"`
	CorrelationID    string `json:"airnz_correlation_id"`
	Env              string `json:"env"`
	EventType        string `json:"event_type"`
	InstanceType     string `json:"instance_type"`
	ClientIP         string `json:"client_ip"`
	APIName          string `json:"api_name"`
	BkHost           string `json:"bk_host"`
	Path             string `json:"path"`
	Status           string `json:"status"`
	RequestMethod    string `json:"request_method"`
	RequestTime      string `json:"request_time"`
	ConsumerUsername string `json:"consumer_username"`
	ResponseStatus   string `json:"response_status"`
	ResponseDuration string `json:"response_duration"`
	AuthType         string `json:"auth_type"`
	AuthSubject      string `json:"auth_subject"`
	ID               string `json:"_id"`
	Type             string `json:"_type"`
	Index            string `json:"_index"`
}

// TimestampTime returns Timestamp as a time.Time.
func (e KongLogEvent) TimestampTime() time.Time {
	return time.UnixMilli(e.Timestamp)
}

// AvroRecord supplies KongLogEvent's exact Avro schema (see
// github.com/heetch/avro/avrotypegen.AvroRecord) instead of letting it be
// inferred by reflection - needed to declare "timestamp" as
// timestamp-millis, which reflection-based inference cannot produce for any
// Go type. Field order here must exactly match the struct field order above
// (Avro's binary encoding is purely positional, not name-keyed). This is
// also what internal/provision's Terraform template renders via
// AvroSchemaJSON, so Schema Registry always matches what the app produces.
func (KongLogEvent) AvroRecord() avrotypegen.RecordInfo {
	return avrotypegen.RecordInfo{
		Schema: `{
			"type": "record",
			"name": "KongLogEvent",
			"namespace": "amd.simulator",
			"fields": [
				{"name": "timestamp", "type": {"type": "long", "logicalType": "timestamp-millis"}},
				{"name": "airnz_correlation_id", "type": "string"},
				{"name": "env", "type": "string"},
				{"name": "event_type", "type": "string"},
				{"name": "instance_type", "type": "string"},
				{"name": "client_ip", "type": "string"},
				{"name": "api_name", "type": "string"},
				{"name": "bk_host", "type": "string"},
				{"name": "path", "type": "string"},
				{"name": "status", "type": "string"},
				{"name": "request_method", "type": "string"},
				{"name": "request_time", "type": "string"},
				{"name": "consumer_username", "type": "string"},
				{"name": "response_status", "type": "string"},
				{"name": "response_duration", "type": "string"},
				{"name": "auth_type", "type": "string"},
				{"name": "auth_subject", "type": "string"},
				{"name": "_id", "type": "string"},
				{"name": "_type", "type": "string"},
				{"name": "_index", "type": "string"}
			]
		}`,
		Required: []bool{
			true, true, true, true, true,
			true, true, true, true, true,
			true, true, true, true, true,
			true, true, true, true, true,
		},
	}
}
