// Package metrics writes CloudWatch metrics in the Embedded Metric Format.
//
// Printing one JSON line per metric is enough: CloudWatch Logs turns it into a
// metric in the Launchpad namespace, with no API call and no extra permission.
package metrics

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"sync"
	"time"
)

const Namespace = "Launchpad"

// Emitter writes metric lines to Out.
type Emitter struct {
	Out io.Writer
	Now func() time.Time

	mu sync.Mutex
}

// Count adds 1 to the named metric. Dimensions are name, value pairs.
func (e *Emitter) Count(name string, dims ...string) {
	e.Emit(name, 1, "Count", dims...)
}

// Emit records a value. Dimensions are name, value pairs.
func (e *Emitter) Emit(name string, value float64, unit string, dims ...string) {
	values := map[string]string{}
	for i := 0; i+1 < len(dims); i += 2 {
		values[dims[i]] = dims[i+1]
	}
	names := make([]string, 0, len(values))
	for k := range values {
		names = append(names, k)
	}
	sort.Strings(names)

	record := map[string]any{
		"_aws": map[string]any{
			"Timestamp": e.Now().UnixMilli(),
			"CloudWatchMetrics": []any{map[string]any{
				"Namespace":  Namespace,
				"Dimensions": [][]string{names},
				"Metrics":    []any{map[string]string{"Name": name, "Unit": unit}},
			}},
		},
		name: value,
	}
	for k, v := range values {
		record[k] = v
	}

	var line bytes.Buffer
	enc := json.NewEncoder(&line)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(record); err != nil {
		return // a metric must never break a request
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, _ = e.Out.Write(line.Bytes())
}
