package terraform

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"launchpad/internal/testenv"
)

var ctx = context.Background()

var plainEnv = []string{"PATH=/usr/bin:/bin"}

func fakeBinary(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "terraform")
	if err := os.WriteFile(path, []byte("#!/bin/bash\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTF(t *testing.T, body string, secrets ...string) *Terraform {
	t.Helper()
	return New(fakeBinary(t, body), t.TempDir(), plainEnv, NewLogTail(secrets...), nil)
}

func TestOutputAndExitCode(t *testing.T) {
	tf := newTF(t, `echo "hello $1"; echo oops >&2; exit 3`)
	r, err := tf.Run(ctx, time.Time{}, "world")
	if err != nil {
		t.Fatal(err)
	}
	if r.Code != 3 || r.Interrupted {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.Output, "hello world") || !strings.Contains(r.Output, "oops") {
		t.Fatalf("output %q", r.Output)
	}
}

func TestALastLineWithoutANewlineIsKept(t *testing.T) {
	tf := newTF(t, `printf 'http://demo.example'`)
	r, _ := tf.Run(ctx, time.Time{})
	if r.Output != "http://demo.example" {
		t.Fatalf("output %q", r.Output)
	}
}

func TestSecretsAreRemovedFromTheLog(t *testing.T) {
	tf := newTF(t, `echo "token is s3cr3t-value here"; echo "ab stays"`, "s3cr3t-value", "ab")
	r, _ := tf.Run(ctx, time.Time{})
	if strings.Contains(r.Output, "s3cr3t-value") || !strings.Contains(r.Output, "token is *** here") {
		t.Fatalf("output %q", r.Output)
	}
	if !strings.Contains(r.Output, "ab stays") {
		t.Fatalf("a secret shorter than four characters is hidden: %q", r.Output)
	}
}

func TestADeadlineInterruptsAndTerraformCanExitCleanly(t *testing.T) {
	tf := newTF(t, "trap 'echo Interrupt received; exit 1' INT\nwhile true; do sleep 0.1; done")
	tf.Grace = 5 * time.Second
	r, err := tf.Run(ctx, time.Now().Add(1200*time.Millisecond), "apply")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Interrupted || r.Code != 1 || !strings.Contains(r.Output, "Interrupt received") {
		t.Fatalf("%+v", r)
	}
}

func TestAProcessThatIgnoresTheInterruptIsKilled(t *testing.T) {
	tf := newTF(t, "trap '' INT\nwhile true; do sleep 0.1; done")
	tf.Grace = 1500 * time.Millisecond
	start := time.Now()
	r, err := tf.Run(ctx, time.Now().Add(time.Second), "apply")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Interrupted || r.Code == 0 || !strings.Contains(r.Output, "killing it") {
		t.Fatalf("%+v", r)
	}
	if took := time.Since(start); took > 15*time.Second {
		t.Fatalf("took %s", took)
	}
}

func TestChildrenOfTerraformAreKilledWithIt(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	// The child ignores the interrupt, as a provider plugin may, and holds the output open.
	tf := newTF(t, "trap '' INT\n(trap '' INT; while true; do sleep 0.1; done) &\necho $! > "+pidFile+"\nwait")
	tf.Grace = time.Second
	start := time.Now()
	if _, err := tf.Run(ctx, time.Now().Add(time.Second), "apply"); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Fatalf("took %s", took)
	}
	pid := strings.TrimSpace(readFile(t, pidFile))
	if out, err := execOutput("kill", "-0", pid); err == nil {
		t.Fatalf("the child %s is still running: %s", pid, out)
	}
}

func TestARunThatFinishesBeforeTheDeadlineIsNotInterrupted(t *testing.T) {
	tf := newTF(t, "exit 0")
	r, err := tf.Run(ctx, time.Now().Add(30*time.Second))
	if err != nil || r.Interrupted || r.Code != 0 {
		t.Fatalf("%+v, %v", r, err)
	}
}

func TestTheProgressCallbackIsCalledWhileRunning(t *testing.T) {
	var ticks atomic.Int32
	tf := New(fakeBinary(t, "sleep 3"), t.TempDir(), plainEnv, NewLogTail(), func() { ticks.Add(1) })
	tf.TickEvery = time.Second
	if _, err := tf.Run(ctx, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if ticks.Load() == 0 {
		t.Fatal("never called")
	}
}

func TestACommandThatCannotStartIsAnError(t *testing.T) {
	tf := New(filepath.Join(t.TempDir(), "missing"), t.TempDir(), plainEnv, NewLogTail(), nil)
	if _, err := tf.Run(ctx, time.Time{}); err == nil {
		t.Fatal("want an error")
	}
}

func TestOnlyTheEnvironmentGivenIsSeen(t *testing.T) {
	t.Setenv("LAUNCHPAD_TEST_LEAK", "leaked")
	tf := newTF(t, `echo "leak=[$LAUNCHPAD_TEST_LEAK] kept=[$KEPT]"`)
	tf.Env = append(tf.Env, "KEPT=yes")
	r, _ := tf.Run(ctx, time.Time{})
	if !strings.Contains(r.Output, "leak=[] kept=[yes]") {
		t.Fatalf("output %q", r.Output)
	}
}

func TestTheTailKeepsTheLastLinesAndCharacters(t *testing.T) {
	tail := NewLogTail()
	for i := range 250 {
		tail.Add(strings.Repeat("x", 5) + string(rune('a'+i%26)))
	}
	if got := strings.Count(tail.Text(), "\n") + 1; got != 200 {
		t.Fatalf("%d lines, want 200", got)
	}
	long := NewLogTail()
	long.Add(strings.Repeat("é", 20000))
	if text := long.Text(); len(text) > 30000 || strings.ContainsRune(text, '�') {
		t.Fatalf("%d bytes, or a cut character", len(text))
	}
}

// --- the binary --------------------------------------------------------------

func zipOf(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("terraform")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("#!/bin/sh\necho hi\n"))
	_ = zw.Close()
	return buf.Bytes()
}

func releaseURL() string {
	return "https://releases.hashicorp.com/terraform/" + Version + "/terraform_" + Version + "_linux_" + runtime.GOARCH + ".zip"
}

func TestTheBinaryDownloadChecksTheChecksum(t *testing.T) {
	env := testenv.New(t)
	data := zipOf(t)
	env.GitHub.Route(releaseURL(), data)
	dir := filepath.Join(t.TempDir(), "bin")

	zero := strings.Repeat("0", 64)
	if _, err := ensureBinary(ctx, env.Services, dir, map[string]string{"amd64": zero, "arm64": zero}); err == nil {
		t.Fatal("a download with the wrong checksum was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "terraform")); err == nil {
		t.Fatal("a binary was installed from a bad download")
	}

	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	path, err := ensureBinary(ctx, env.Services, dir, map[string]string{"amd64": digest, "arm64": digest})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode()&0o100 == 0 {
		t.Fatalf("binary %v, %v", info, err)
	}

	env.GitHub.Clear() // a second call must not download again
	again, err := ensureBinary(ctx, env.Services, dir, map[string]string{})
	if err != nil || again != path {
		t.Fatalf("%q, %v", again, err)
	}
}

func TestTheBinaryDownloadReportsAMissingRelease(t *testing.T) {
	env := testenv.New(t)
	sums := map[string]string{"amd64": "x", "arm64": "x"}
	if _, err := ensureBinary(ctx, env.Services, t.TempDir(), sums); err == nil {
		t.Fatal("want an error for a 404")
	}
}

func TestTheShippedChecksumsAreForTheArchitecturesLambdaRuns(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		if len(checksums[arch]) != 64 {
			t.Errorf("no SHA-256 for %s", arch)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func execOutput(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}
