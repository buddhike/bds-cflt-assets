// Package lightning talks to the Confluent Cloud Lightning Table APIs documented at
// https://docs.confluent.io/cloud/current/lightning-tables/ : the query endpoint and the
// rtce-topics list that gives a table's status. Terraform enables and disables the table.
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

// inListMax is the most values in one IN list: the table accepts up to 20 (the owner's
// reading of the documentation). A longer list is refused with 400 QE_UNSUPPORTED_SUBQUERY
// (seen against a live table with about 500 locations), so a set is split into lists of at
// most 20 joined with OR, which is the same predicate.
const inListMax = 20

// LocationsQuery is scenario by_locations: one SKU at a set of locations (at least one). The
// values come from the catalogue's own generator, never from the browser, so they are not
// escaped.
func LocationsQuery(topic, sku string, locations []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "SELECT sku, location_id, quantity FROM `%s` WHERE sku = '%s' AND ", topic, sku)
	groups := (len(locations) + inListMax - 1) / inListMax
	if groups > 1 {
		b.WriteByte('(')
	}
	for g := 0; g < groups; g++ {
		if g > 0 {
			b.WriteString(" OR ")
		}
		b.WriteString("location_id IN (")
		for i, l := range locations[g*inListMax : min((g+1)*inListMax, len(locations))] {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteByte('\'')
			b.WriteString(l)
			b.WriteByte('\'')
		}
		b.WriteByte(')')
	}
	if groups > 1 {
		b.WriteByte(')')
	}
	return b.String()
}
