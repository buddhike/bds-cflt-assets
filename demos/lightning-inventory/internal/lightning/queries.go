// Package lightning talks to the Confluent Cloud Lightning Table APIs documented at
// https://docs.confluent.io/cloud/current/lightning-tables/ : the query endpoint and the
// rtce-topics endpoint that reads a table's status. Terraform enables and disables the table.
package lightning

import (
	"fmt"
	"regexp"
	"strings"
)

// A Lightning Table's topic name is letters, digits and underscores, and starting with a
// letter (the API's own pattern), so it is safe inside backticks.
var topicName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

// ValidTopic reports whether name can be a Lightning Table topic.
func ValidTopic(name string) bool { return topicName.MatchString(name) }

// SKUQuery is scenario by_sku: all locations for one SKU. The SKU comes from the
// catalogue's own generator, never from the browser.
func SKUQuery(topic, sku string) string {
	return fmt.Sprintf("SELECT sku, location_id, quantity FROM `%s` WHERE sku = '%s'", topic, sku)
}

// LocationsQuery is scenario by_locations: one SKU at a set of locations.
func LocationsQuery(topic, sku string, locations []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "SELECT sku, location_id, quantity FROM `%s` WHERE sku = '%s' AND location_id IN (", topic, sku)
	for i, l := range locations {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('\'')
		b.WriteString(l)
		b.WriteByte('\'')
	}
	b.WriteByte(')')
	return b.String()
}
