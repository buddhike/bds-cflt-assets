// Package health consumes the api_unhealthy_events topic that the
// Confluent Cloud Flink health-monitoring pipeline
// (tf/flink/detect_anomalies.sql) produces to, decoding Avro
// wire-format events for the MONITOR tab.
package health

import "time"

// HealthEvent mirrors one row of api_unhealthy_events: an API's health in one
// 10-second window. IsAnomaly says whether its unhealthy rate was outside the
// detector's bounds. A window with no failing calls has one row with HTTPStatus
// "RECOVERED"; otherwise there is a row per failing status code.
//
// DetectedAt is a plain int64 (Unix milliseconds), not time.Time: Flink
// serializes TIMESTAMP_LTZ using Avro's local-timestamp-millis logical
// type, but heetch/avro (the codec confluent-kafka-go's Avro
// deserializer uses internally) decides how to interpret a decoded long
// purely from the Go target field's type, not from the schema's logical
// type string - a time.Time field is unconditionally treated as
// timestamp-micros regardless of what the schema actually says, which
// would silently misinterpret a millisecond value as microseconds (a
// 1000x error). int64 sidesteps that by taking the raw value as-is; use
// DetectedAtTime() for a time.Time view.
type HealthEvent struct {
	APIID          string   `json:"api_id"`
	DetectedAt     int64    `json:"detected_at"`
	HTTPStatus     string   `json:"http_status"`
	StatusCount    int64    `json:"status_count"`
	TotalCount     int64    `json:"total_count"`
	UnhealthyCount int64    `json:"unhealthy_count"`
	UnhealthyRate  float64  `json:"unhealthy_rate"`
	ForecastValue  *float64 `json:"forecast_value"`
	UpperBound     *float64 `json:"upper_bound"`
	LowerBound     *float64 `json:"lower_bound"`
	IsAnomaly      bool     `json:"is_anomaly"`
}

// DetectedAtTime returns DetectedAt as a time.Time.
func (e HealthEvent) DetectedAtTime() time.Time {
	return time.UnixMilli(e.DetectedAt)
}
