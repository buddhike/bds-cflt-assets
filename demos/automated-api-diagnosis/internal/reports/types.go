// Package reports consumes the api_anomaly_ai_reports topic that a
// Confluent Cloud Flink job (using an AI agent) writes one row to for each
// detected API anomaly, decoding the Avro wire-format values for the
// AI REPORTS tab.
package reports

import (
	"regexp"
	"strings"
	"time"
)

// row mirrors one row of api_anomaly_ai_reports exactly as it is encoded in
// the Kafka message value. The table declares no PRIMARY KEY, so Flink
// writes a null message key and every column lives in the value - unlike
// internal/health, there is no key to decode.
//
// DetectedAt is a plain int64 (Unix milliseconds) for the same reason as
// health.HealthEvent.DetectedAt: heetch/avro decides how to read a long
// from the Go field's type rather than the schema's logical type, and a
// time.Time field would misread TIMESTAMP_LTZ(3) millis as micros.
type row struct {
	APIID       string `json:"api_id"`
	HTTPStatus  string `json:"http_status"`
	DetectedAt  int64  `json:"detected_at"`
	AgentStatus string `json:"agent_status"`
	Report      string `json:"report"`
}

// Report is one anomaly report as handed to the frontend.
type Report struct {
	// ID uniquely identifies the Kafka record ("partition-offset"), so the
	// frontend has a stable key for list rendering and selection even when
	// two reports share api_id/http_status/detected_at.
	ID          string `json:"id"`
	APIID       string `json:"api_id"`
	HTTPStatus  string `json:"http_status"`
	DetectedAt  int64  `json:"detected_at"`
	AgentStatus string `json:"agent_status"`
	// Report is the agent's markdown with <reasoning> blocks removed.
	Report string `json:"report"`
}

// DetectedAtTime returns DetectedAt as a time.Time.
func (r Report) DetectedAtTime() time.Time {
	return time.UnixMilli(r.DetectedAt)
}

var reasoningBlock = regexp.MustCompile(`(?is)<reasoning>.*?</reasoning>`)

// StripReasoning removes every <reasoning>...</reasoning> block (the AI
// agent's chain of thought, which isn't meant to be shown) and trims the
// whitespace left behind. The match is case-insensitive and spans lines.
func StripReasoning(report string) string {
	return strings.TrimSpace(reasoningBlock.ReplaceAllString(report, ""))
}
