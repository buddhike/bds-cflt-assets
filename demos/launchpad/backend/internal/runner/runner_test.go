package runner_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"launchpad/internal/catalog"
	"launchpad/internal/credentials"
	"launchpad/internal/runner"
	"launchpad/internal/runs"
	"launchpad/internal/testenv"
	"launchpad/internal/users"
)

var ctx = context.Background()

var ann = &users.User{Email: "ann@confluent.io", Role: "user"}

const fakeTerraform = `#!/bin/bash
FAKE_LOG=@LOG@; FAKE_LOCK=@LOCK@; FAKE_MODE=@MODE@
echo "terraform $@" >> "$FAKE_LOG"
case "$1" in
  init) exit 0 ;;
  output) printf '%s' '@URL@'; exit 0 ;;
  force-unlock) rm -f "$FAKE_LOCK"; exit 0 ;;
  apply|destroy)
    if [ -f "$FAKE_LOCK" ]; then
      echo "Error: Error acquiring the state lock"; echo "Lock Info:"; echo "  ID:        6f1c1c1e-1111-2222-3333-444455556666"; exit 1
    fi
    case "$FAKE_MODE" in
      ok) echo "Apply complete. key=$TF_VAR_confluent_cloud_api_key secret=$TF_VAR_confluent_cloud_api_secret"; exit 0 ;;
      fail) echo "Error: boom"; exit 1 ;;
      slow) trap 'echo Interrupt received; exit 1' INT; while true; do sleep 0.1; done ;;
    esac ;;
esac
`

type fixture struct {
	*testenv.Env
	runner *runner.Runner
	tmp    string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	env := testenv.New(t)
	tmp := t.TempDir()
	f := &fixture{Env: env, tmp: tmp}
	f.runner = runner.New(env.Services, filepath.Join(tmp, "work"))
	if err := os.MkdirAll(filepath.Join(tmp, "work", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.install("ok", "http://demo.example")

	env.GitHub.AddRepo(map[string]string{"demos/pims/tf/main.tf": testenv.CloudTF, "demos/plain/tf/main.tf": testenv.GoodTF})
	top := "r-" + testenv.SHA + "/"
	env.GitHub.Tarball([]testenv.Member{
		{Name: top + "demos/pims/tf/main.tf", Data: testenv.CloudTF},
		{Name: top + "demos/plain/tf/main.tf", Data: testenv.GoodTF},
	})
	if _, err := catalog.Refresh(ctx, env.Services); err != nil {
		t.Fatal(err)
	}
	return f
}

// install puts the fake Terraform in place. The runner hides the test's
// environment from it, so the settings are baked into the script.
func (f *fixture) install(mode, url string) {
	script := strings.NewReplacer(
		"@LOG@", filepath.Join(f.tmp, "calls.log"), "@LOCK@", filepath.Join(f.tmp, "lock"), "@MODE@", mode, "@URL@", url,
	).Replace(fakeTerraform)
	if err := os.WriteFile(filepath.Join(f.tmp, "work", "bin", "terraform"), []byte(script), 0o755); err != nil {
		panic(err)
	}
}

func (f *fixture) calls() []string {
	data, err := os.ReadFile(filepath.Join(f.tmp, "calls.log"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func (f *fixture) called(prefix string) int {
	n := 0
	for _, c := range f.calls() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fixture) state(demo string) *runs.State {
	state, err := runs.GetRaw(ctx, f.Services, demo)
	if err != nil {
		panic(err)
	}
	return state
}

func (f *fixture) launch(t *testing.T, demo string) runner.Event {
	t.Helper()
	const email = "ann@confluent.io"
	if demo == "pims" {
		if _, err := credentials.Save(ctx, f.Services, email, "KEY12345", "hunter2hunter2"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runs.RequestLaunch(ctx, f.Services, demo, email); err != nil {
		t.Fatal(err)
	}
	f.Lambda.Clear()
	return runner.Event{DemoID: demo, RunID: f.state(demo).RunID}
}

func (f *fixture) run(t *testing.T, ev runner.Event, left time.Duration) string {
	t.Helper()
	result, err := f.runner.Run(ctx, ev, func() time.Duration { return left })
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestASuccessfulLaunchReadsTheURL(t *testing.T) {
	f := newFixture(t)
	ev := f.launch(t, "plain")
	if got := f.run(t, ev, 900*time.Second); got != "running" {
		t.Fatalf("result %q", got)
	}
	st := f.state("plain").Public()
	if st.Status != "running" || st.URL != "http://demo.example" {
		t.Fatalf("state %+v", st)
	}
	calls := f.calls()
	if !strings.HasPrefix(calls[0], "terraform init") || !strings.Contains(calls[0], "-backend-config=key=demos/plain/terraform.tfstate") {
		t.Fatalf("first call %q", calls[0])
	}
	if f.called("terraform apply -auto-approve") != 1 {
		t.Fatalf("calls %v", calls)
	}
	backend, err := os.ReadFile(filepath.Join(f.runner.Work, "src/plain/tf/zz_launchpad_backend.tf"))
	if err != nil || !strings.Contains(string(backend), `backend "s3"`) {
		t.Fatalf("backend file %q, %v", backend, err)
	}
	if !strings.Contains(f.Emitted(), `"DemoLaunchSucceeded":1`) || !strings.Contains(f.Emitted(), `"TerraformSeconds"`) {
		t.Fatalf("metrics %s", f.Emitted())
	}
}

func TestADemoWithNoAddressRunsWithAnEmptyOne(t *testing.T) {
	f := newFixture(t)
	f.install("ok", "")
	ev := f.launch(t, "plain")
	if got := f.run(t, ev, 900*time.Second); got != "running" {
		t.Fatalf("result %q", got)
	}
	// The address must not be a leftover line of the apply's output.
	if url := f.state("plain").URL; url != "" {
		t.Fatalf("url %q, want none", url)
	}
}

func TestTheDirectoriesTerraformIsPointedAtExist(t *testing.T) {
	f := newFixture(t)
	f.run(t, f.launch(t, "plain"), 900*time.Second)
	env, err := f.runner.BuildEnv("plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"TF_PLUGIN_CACHE_DIR", "TF_DATA_DIR", "HOME"} {
		if info, err := os.Stat(env[name]); err != nil || !info.IsDir() {
			t.Errorf("%s points at %s, which does not exist", name, env[name])
		}
	}
}

func TestCredentialsReachTerraformButNeverTheLog(t *testing.T) {
	f := newFixture(t)
	f.run(t, f.launch(t, "pims"), 900*time.Second)
	tail := f.state("pims").LogTail
	if strings.Contains(tail, "hunter2hunter2") || strings.Contains(tail, "KEY12345") || !strings.Contains(tail, "secret=***") {
		t.Fatalf("log tail %q", tail)
	}
}

func TestTheEnvironmentForTerraformIsMinimal(t *testing.T) {
	f := newFixture(t)
	t.Setenv("GITHUB_TOKEN", "ghp_leak")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIA")
	env, err := f.runner.BuildEnv("pims", &credentials.Credentials{APIKey: "K", APISecret: "S"})
	if err != nil {
		t.Fatal(err)
	}
	if _, leaked := env["GITHUB_TOKEN"]; leaked || env["AWS_ACCESS_KEY_ID"] != "AKIA" {
		t.Fatalf("env %v", env)
	}
	if env["TF_VAR_launchpad_tags"] != `{"launchpad:demo":"pims","launchpad:managed":"true"}` {
		t.Errorf("tags %s", env["TF_VAR_launchpad_tags"])
	}
	if env["TF_VAR_aws_region"] != "us-east-1" || env["CONFLUENT_CLOUD_API_KEY"] != "K" || env["TF_VAR_launchpad_demo_id"] != "pims" {
		t.Errorf("env %v", env)
	}
	plain, _ := f.runner.BuildEnv("plain", nil)
	if _, ok := plain["CONFLUENT_CLOUD_API_KEY"]; ok {
		t.Error("credentials were passed to a demo that has none")
	}
}

func TestAFailedApplyRecordsTheError(t *testing.T) {
	f := newFixture(t)
	f.install("fail", "")
	ev := f.launch(t, "plain")
	if got := f.run(t, ev, 900*time.Second); got != "failed" {
		t.Fatalf("result %q", got)
	}
	st := f.state("plain").Public()
	if st.Status != "launch_failed" || !strings.Contains(st.Error, "terraform apply failed") || !strings.Contains(st.LogTail, "Error: boom") {
		t.Fatalf("state %+v", st)
	}
	if !strings.Contains(f.Emitted(), `"DemoLaunchFailed":1`) || !strings.Contains(f.Logs(), "Run failed") {
		t.Fatalf("metrics %s, logs %s", f.Emitted(), f.Logs())
	}
}

func TestTeardownRemovesTheState(t *testing.T) {
	f := newFixture(t)
	f.run(t, f.launch(t, "plain"), 900*time.Second)
	if _, err := runs.RequestTeardown(ctx, f.Services, "plain", ann); err != nil {
		t.Fatal(err)
	}
	ev := runner.Event{DemoID: "plain", RunID: f.state("plain").RunID}
	if got := f.run(t, ev, 900*time.Second); got != "destroyed" {
		t.Fatalf("result %q", got)
	}
	if f.state("plain") != nil {
		t.Fatal("the state is still there")
	}
	if f.called("terraform destroy -auto-approve") != 1 {
		t.Fatalf("calls %v", f.calls())
	}
}

func TestAFailedTeardownIsRecorded(t *testing.T) {
	f := newFixture(t)
	f.run(t, f.launch(t, "plain"), 900*time.Second)
	f.install("fail", "")
	_, _ = runs.RequestTeardown(ctx, f.Services, "plain", ann)
	ev := runner.Event{DemoID: "plain", RunID: f.state("plain").RunID}
	if got := f.run(t, ev, 900*time.Second); got != "failed" {
		t.Fatalf("result %q", got)
	}
	if got := f.state("plain").Status; got != runs.TeardownFailed {
		t.Fatalf("status %q", got)
	}
	if !strings.Contains(f.Emitted(), `"DemoTeardownFailed":1`) {
		t.Fatalf("metrics %s", f.Emitted())
	}
}

func TestAStaleInvocationDoesNothing(t *testing.T) {
	f := newFixture(t)
	ev := f.launch(t, "plain")
	ev.RunID = "old"
	if got := f.run(t, ev, 900*time.Second); got != "stale" {
		t.Fatalf("result %q", got)
	}
	if len(f.calls()) != 0 {
		t.Fatalf("calls %v", f.calls())
	}
	if got := f.run(t, runner.Event{DemoID: "plain", RunID: "x"}, time.Hour); got != "stale" {
		t.Fatalf("result %q", got)
	}
	if got := f.run(t, runner.Event{DemoID: "pims", RunID: "x"}, time.Hour); got != "stale" {
		t.Fatalf("a demo that is not deployed: %q", got)
	}
}

func TestARunThatRunsOutOfTimeResumesInANewInvocation(t *testing.T) {
	f := newFixture(t)
	f.install("slow", "")
	f.runner.Reserve, f.runner.MinUseful, f.runner.Grace = 0, 0, 5*time.Second
	ev := f.launch(t, "plain")
	start := time.Now()
	if got := f.run(t, ev, time.Second); got != "resumed" {
		t.Fatalf("result %q", got)
	}
	if took := time.Since(start); took > 30*time.Second {
		t.Fatalf("took %s", took)
	}
	calls := f.Lambda.Calls()
	if len(calls) != 1 || calls[0].Payload["demoId"] != "plain" || calls[0].Payload["runId"] != ev.RunID || calls[0].Payload["resumes"] != float64(1) {
		t.Fatalf("invocations %+v", calls)
	}
	st := f.state("plain")
	if st.Status != "launching" || st.Resumes != 1 || !strings.Contains(st.LogTail, "continuing in a new invocation") {
		t.Fatalf("state %+v", st)
	}
}

func TestItGivesUpAfterTooManyResumes(t *testing.T) {
	f := newFixture(t)
	f.install("slow", "")
	f.runner.Reserve, f.runner.MinUseful = 0, 0
	ev := f.launch(t, "plain")
	ev.Resumes = f.runner.MaxResumes
	if got := f.run(t, ev, time.Second); got != "failed" {
		t.Fatalf("result %q", got)
	}
	st := f.state("plain")
	if st.Status != runs.LaunchFailed || !strings.Contains(st.Error, "Not finished after 9 attempts") {
		t.Fatalf("state %+v", st)
	}
	if len(f.Lambda.Calls()) != 0 {
		t.Fatal("the run was resumed again")
	}
}

func TestTooLittleTimeLeftResumesWithoutRunningApply(t *testing.T) {
	f := newFixture(t)
	ev := f.launch(t, "plain")
	if got := f.run(t, ev, 100*time.Second); got != "resumed" {
		t.Fatalf("result %q", got)
	}
	if f.called("terraform apply") != 0 {
		t.Fatalf("calls %v", f.calls())
	}
}

func TestAStaleLockFromACutOffSliceIsCleared(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.tmp, "lock"), []byte("held"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev := f.launch(t, "plain")
	if got := f.run(t, ev, 900*time.Second); got != "running" {
		t.Fatalf("result %q", got)
	}
	if f.called("terraform force-unlock -force 6f1c1c1e") != 1 || f.called("terraform apply") != 2 {
		t.Fatalf("calls %v", f.calls())
	}
}

func TestMissingCredentialsFailTheRunClearly(t *testing.T) {
	f := newFixture(t)
	ev := f.launch(t, "pims")
	if err := credentials.Delete(ctx, f.Services, "ann@confluent.io"); err != nil {
		t.Fatal(err)
	}
	if got := f.run(t, ev, 900*time.Second); got != "failed" {
		t.Fatalf("result %q", got)
	}
	if !strings.Contains(f.state("pims").Error, "credentials are gone") {
		t.Fatalf("error %q", f.state("pims").Error)
	}
	if len(f.calls()) != 0 {
		t.Fatalf("calls %v", f.calls())
	}
}

func TestADemoThatBreaksTheInterfaceFailsTheRun(t *testing.T) {
	f := newFixture(t)
	f.Env.GitHub.Tarball([]testenv.Member{{Name: "r-" + testenv.SHA + "/demos/plain/tf/main.tf", Data: `resource "x" "y" {}`}})
	ev := f.launch(t, "plain")
	if got := f.run(t, ev, 900*time.Second); got != "failed" {
		t.Fatalf("result %q", got)
	}
	if !strings.Contains(f.state("plain").Error, "does not follow the Launchpad interface") {
		t.Fatalf("error %q", f.state("plain").Error)
	}
}

func TestAnErrorFromTheRepositoryFailsTheRunWithItsMessage(t *testing.T) {
	f := newFixture(t)
	ev := f.launch(t, "plain")
	f.GitHub.Clear()
	if got := f.run(t, ev, 900*time.Second); got != "failed" {
		t.Fatalf("result %q", got)
	}
	if msg := f.state("plain").Error; !strings.Contains(msg, "GitHub") {
		t.Fatalf("error %q", msg)
	}
}
