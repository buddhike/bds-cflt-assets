package simulator

import "github.com/heetch/avro"

// AvroSchemaJSON returns the Avro schema text for KongLogEvent, resolved
// the exact same way the confluent-kafka-go serializer resolves it at
// runtime (avro.TypeOf, which defers to KongLogEvent's AvroRecord method
// rather than inferring a schema by reflection - see types.go). Terraform
// renders this same text into the confluent_schema resource (see
// internal/provision) so what gets registered ahead of time matches what
// the running app registers (idempotently) on first produce.
func AvroSchemaJSON() (string, error) {
	t, err := avro.TypeOf(KongLogEvent{})
	if err != nil {
		return "", err
	}
	return t.String(), nil
}
