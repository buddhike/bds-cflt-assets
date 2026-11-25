package metrics

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func emitter() (*Emitter, *bytes.Buffer) {
	var out bytes.Buffer
	return &Emitter{Out: &out, Now: func() time.Time { return time.UnixMilli(1_700_000_000_123) }}, &out
}

func decode(t *testing.T, out *bytes.Buffer) map[string]any {
	t.Helper()
	if strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("want one line, got %q", out.String())
	}
	var rec map[string]any
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestEmitWritesOneEMFLine(t *testing.T) {
	e, out := emitter()
	e.Count("DemoLaunchRequested", "demo", "pims")
	rec := decode(t, out)
	aws := rec["_aws"].(map[string]any)
	cw := aws["CloudWatchMetrics"].([]any)[0].(map[string]any)
	if cw["Namespace"] != "Launchpad" {
		t.Errorf("namespace %v", cw["Namespace"])
	}
	if dims, _ := json.Marshal(cw["Dimensions"]); string(dims) != `[["demo"]]` {
		t.Errorf("dimensions %s", dims)
	}
	if m, _ := json.Marshal(cw["Metrics"]); string(m) != `[{"Name":"DemoLaunchRequested","Unit":"Count"}]` {
		t.Errorf("metrics %s", m)
	}
	if rec["DemoLaunchRequested"] != float64(1) || rec["demo"] != "pims" || aws["Timestamp"] != float64(1_700_000_000_123) {
		t.Errorf("record %v", rec)
	}
}

func TestEmitWithoutDimensions(t *testing.T) {
	e, out := emitter()
	e.Count("SignInStarted")
	cw := decode(t, out)["_aws"].(map[string]any)["CloudWatchMetrics"].([]any)[0].(map[string]any)
	if dims, _ := json.Marshal(cw["Dimensions"]); string(dims) != `[[]]` {
		t.Errorf("dimensions %s", dims)
	}
}

func TestDimensionsAreSortedAndValuesKeepTheirUnit(t *testing.T) {
	e, out := emitter()
	e.Emit("TerraformSeconds", 12.5, "Seconds", "demo", "pims", "action", "apply")
	rec := decode(t, out)
	cw := rec["_aws"].(map[string]any)["CloudWatchMetrics"].([]any)[0].(map[string]any)
	if dims, _ := json.Marshal(cw["Dimensions"]); string(dims) != `[["action","demo"]]` {
		t.Errorf("dimensions %s", dims)
	}
	if rec["TerraformSeconds"] != 12.5 || rec["action"] != "apply" {
		t.Errorf("record %v", rec)
	}
}
