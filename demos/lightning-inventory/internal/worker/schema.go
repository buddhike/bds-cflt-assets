package worker

import (
	"encoding/binary"
	"fmt"

	"github.com/hamba/avro/v2"
)

// SchemaJSON is the Avro value schema of the topic. tf/inventory-value.avsc is the same
// schema (contract_test.go keeps them equal), registered by Terraform before the topic
// exists. All fields are required, so the Lightning Table's columns are NOT NULL.
const SchemaJSON = `{
  "type": "record",
  "name": "InventoryPosition",
  "namespace": "lightning.inventory",
  "fields": [
    {"name": "sku", "type": "string"},
    {"name": "location_id", "type": "string"},
    {"name": "quantity", "type": "int"}
  ]
}`

// Position is one inventory position: the latest quantity of a SKU at a location.
type Position struct {
	SKU        string `avro:"sku"`
	LocationID string `avro:"location_id"`
	Quantity   int32  `avro:"quantity"`
}

// Codec encodes positions in the Confluent wire format: a zero byte, the schema id as 4
// big-endian bytes, then Avro binary.
type Codec struct {
	schema avro.Schema
	id     uint32
}

// NewCodec returns a Codec for the schema Terraform registered under id.
func NewCodec(schemaID int) (*Codec, error) {
	if schemaID <= 0 {
		return nil, fmt.Errorf("schema id %d is not valid", schemaID)
	}
	s, err := avro.Parse(SchemaJSON)
	if err != nil {
		return nil, err
	}
	return &Codec{schema: s, id: uint32(schemaID)}, nil
}

// Encode returns the record value.
func (c *Codec) Encode(p Position) ([]byte, error) {
	body, err := avro.Marshal(c.schema, p)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 5, 5+len(body))
	binary.BigEndian.PutUint32(out[1:], c.id)
	return append(out, body...), nil
}
