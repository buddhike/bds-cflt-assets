package terraform

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"

	"pims/internal/config"
)

// TestEmbeddedConfigValidates downloads terraform and the Confluent
// provider, then runs terraform validate. It needs network access, so it
// only runs when PIMS_INTEGRATION=1.
func TestEmbeddedConfigValidates(t *testing.T) {
	if os.Getenv("PIMS_INTEGRATION") != "1" {
		t.Skip("set PIMS_INTEGRATION=1 to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ws := config.Workspace{Root: t.TempDir()}
	if err := ws.Ensure(); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(ws, discardLogger())
	execPath, err := r.install(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeConfig(ws.TerraformDir()); err != nil {
		t.Fatal(err)
	}
	tf, err := tfexec.NewTerraform(ws.TerraformDir(), execPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := tf.Init(ctx, tfexec.Backend(false)); err != nil {
		t.Fatalf("init: %v", err)
	}
	v, err := tf.Validate(ctx)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !v.Valid {
		for _, d := range v.Diagnostics {
			t.Errorf("%s: %s: %s", d.Severity, d.Summary, d.Detail)
		}
		t.Fatal("embedded terraform configuration is invalid")
	}
}
