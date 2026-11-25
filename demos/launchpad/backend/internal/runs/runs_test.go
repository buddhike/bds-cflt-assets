package runs_test

import (
	"context"
	"errors"
	"testing"

	"launchpad/internal/apperr"
	"launchpad/internal/catalog"
	"launchpad/internal/credentials"
	"launchpad/internal/runs"
	"launchpad/internal/testenv"
	"launchpad/internal/users"
)

var ctx = context.Background()

var (
	ann  = &users.User{Email: "ann@confluent.io", Role: "user"}
	bob  = &users.User{Email: "bob@confluent.io", Role: "user"}
	boss = &users.User{Email: "boss@confluent.io", Role: users.Admin}
)

func fixture(t *testing.T) *testenv.Env {
	env := testenv.New(t)
	env.GitHub.AddRepo(map[string]string{
		"demos/pims/tf/main.tf": testenv.CloudTF, "demos/plain/tf/main.tf": testenv.GoodTF, "demos/bad/tf/main.tf": "",
	})
	if _, err := catalog.Refresh(ctx, env.Services); err != nil {
		t.Fatal(err)
	}
	return env
}

func withCreds(t *testing.T, env *testenv.Env, email string) {
	t.Helper()
	if _, err := credentials.Save(ctx, env.Services, email, "KEY12345", "secretsecret"); err != nil {
		t.Fatal(err)
	}
}

func wantAPIError(t *testing.T, err error, status int, code string) {
	t.Helper()
	appErr, ok := apperr.As(err)
	if !ok || appErr.Status != status || (code != "" && appErr.Code != code) {
		t.Fatalf("got %v, want %d %s", err, status, code)
	}
}

func raw(t *testing.T, env *testenv.Env, demo string) *runs.State {
	t.Helper()
	state, err := runs.GetRaw(ctx, env.Services, demo)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func launched(t *testing.T, env *testenv.Env, demo, email string) string {
	t.Helper()
	if _, err := runs.RequestLaunch(ctx, env.Services, demo, email); err != nil {
		t.Fatal(err)
	}
	run := raw(t, env, demo).RunID
	if ok, err := runs.Finish(ctx, env.Services, demo, run, runs.Running, runs.Outcome{URL: "http://x"}); !ok || err != nil {
		t.Fatalf("finish: %v, %v", ok, err)
	}
	return run
}

func TestLaunchRecordsStateAndStartsTheRunner(t *testing.T) {
	env := fixture(t)
	withCreds(t, env, ann.Email)
	out, err := runs.RequestLaunch(ctx, env.Services, "pims", ann.Email)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "launching" || out.LaunchedBy != ann.Email || out.Ref != testenv.SHA {
		t.Fatalf("state %+v", out)
	}
	calls := env.Lambda.Calls()
	if len(calls) != 1 {
		t.Fatalf("%d invocations", len(calls))
	}
	c := calls[0]
	if c.Type != "Event" || c.Function != "launchpad-runner" || c.Payload["demoId"] != "pims" || c.Payload["runId"] != raw(t, env, "pims").RunID {
		t.Fatalf("invocation %+v", c)
	}
}

func TestLaunchNeedsCredentialsOnlyWhenTheDemoDoes(t *testing.T) {
	env := fixture(t)
	_, err := runs.RequestLaunch(ctx, env.Services, "pims", ann.Email)
	wantAPIError(t, err, 409, "credentials_required")
	if len(env.Lambda.Calls()) != 0 {
		t.Fatal("the runner was started")
	}
	if out, err := runs.RequestLaunch(ctx, env.Services, "plain", ann.Email); err != nil || out.Status != "launching" {
		t.Fatalf("%+v, %v", out, err)
	}
}

func TestUnknownAndInvalidDemos(t *testing.T) {
	env := fixture(t)
	_, err := runs.RequestLaunch(ctx, env.Services, "nope", ann.Email)
	wantAPIError(t, err, 404, "")
	_, err = runs.RequestLaunch(ctx, env.Services, "bad", ann.Email)
	wantAPIError(t, err, 409, "invalid_demo")
}

func TestASecondLaunchIsRefusedWhileRunning(t *testing.T) {
	env := fixture(t)
	_, _ = runs.RequestLaunch(ctx, env.Services, "plain", ann.Email)
	_, err := runs.RequestLaunch(ctx, env.Services, "plain", bob.Email)
	wantAPIError(t, err, 409, "not_launchable")
	if n := len(env.Lambda.Calls()); n != 1 {
		t.Fatalf("%d invocations, want 1", n)
	}
}

func TestAFailedLaunchCanBeRetried(t *testing.T) {
	env := fixture(t)
	_, _ = runs.RequestLaunch(ctx, env.Services, "plain", ann.Email)
	run := raw(t, env, "plain").RunID
	if ok, _ := runs.Finish(ctx, env.Services, "plain", run, runs.LaunchFailed, runs.Outcome{Error: "boom"}); !ok {
		t.Fatal("finish did not apply")
	}
	if got := raw(t, env, "plain").Public().Error; got != "boom" {
		t.Fatalf("error %q", got)
	}
	if _, err := runs.RequestLaunch(ctx, env.Services, "plain", bob.Email); err != nil {
		t.Fatal(err)
	}
	if got := raw(t, env, "plain").LaunchedBy; got != bob.Email {
		t.Fatalf("launched by %q", got)
	}
}

func TestARunnerThatCannotStartLeavesAFailedLaunch(t *testing.T) {
	env := fixture(t)
	env.Lambda.Err = errors.New("no")
	_, err := runs.RequestLaunch(ctx, env.Services, "plain", ann.Email)
	wantAPIError(t, err, 502, "runner_unavailable")
	if got := raw(t, env, "plain").Status; got != runs.LaunchFailed {
		t.Fatalf("status %q", got)
	}
}

func TestTeardownByTheLauncherAndByAnAdminButNotByOthers(t *testing.T) {
	env := fixture(t)
	launched(t, env, "plain", ann.Email)
	_, err := runs.RequestTeardown(ctx, env.Services, "plain", bob)
	wantAPIError(t, err, 403, "")
	out, err := runs.RequestTeardown(ctx, env.Services, "plain", boss)
	if err != nil || out.Status != "tearing_down" {
		t.Fatalf("%+v, %v", out, err)
	}
	calls := env.Lambda.Calls()
	if got := calls[len(calls)-1].Payload["runId"]; got != raw(t, env, "plain").RunID {
		t.Fatalf("invoked with run %v", got)
	}
}

func TestTeardownConditions(t *testing.T) {
	env := fixture(t)
	_, err := runs.RequestTeardown(ctx, env.Services, "plain", ann)
	wantAPIError(t, err, 404, "")
	_, _ = runs.RequestLaunch(ctx, env.Services, "plain", ann.Email) // still launching
	_, err = runs.RequestTeardown(ctx, env.Services, "plain", ann)
	wantAPIError(t, err, 409, "not_tearable")
}

func TestTeardownUsesTheLaunchersCredentialsOrFallsBackToTheRequester(t *testing.T) {
	env := fixture(t)
	withCreds(t, env, ann.Email)
	launched(t, env, "pims", ann.Email)
	if _, err := runs.RequestTeardown(ctx, env.Services, "pims", boss); err != nil { // boss has none, ann still does
		t.Fatal(err)
	}
	if got := raw(t, env, "pims").CredsOwner; got != ann.Email {
		t.Fatalf("credentials of %q", got)
	}
	run := raw(t, env, "pims").RunID
	_, _ = runs.Finish(ctx, env.Services, "pims", run, runs.TeardownFailed, runs.Outcome{Error: "x"})
	_ = credentials.Delete(ctx, env.Services, ann.Email)
	_, err := runs.RequestTeardown(ctx, env.Services, "pims", boss)
	wantAPIError(t, err, 409, "credentials_required")
	withCreds(t, env, boss.Email)
	if _, err := runs.RequestTeardown(ctx, env.Services, "pims", boss); err != nil {
		t.Fatal(err)
	}
	if got := raw(t, env, "pims").CredsOwner; got != boss.Email {
		t.Fatalf("credentials of %q", got)
	}
}

func TestAStaleRunnerCannotChangeANewerRun(t *testing.T) {
	env := fixture(t)
	_, _ = runs.RequestLaunch(ctx, env.Services, "plain", ann.Email)
	old := raw(t, env, "plain").RunID
	_, _ = runs.Finish(ctx, env.Services, "plain", old, runs.LaunchFailed, runs.Outcome{Error: "x"})
	_, _ = runs.RequestLaunch(ctx, env.Services, "plain", ann.Email)
	if ok, err := runs.Finish(ctx, env.Services, "plain", old, runs.Running, runs.Outcome{URL: "http://stale"}); ok || err != nil {
		t.Fatalf("a stale finish applied: %v, %v", ok, err)
	}
	if ok, err := runs.UpdateLog(ctx, env.Services, "plain", old, "stale log"); ok || err != nil {
		t.Fatalf("a stale log applied: %v, %v", ok, err)
	}
	if ok, err := runs.FinishGone(ctx, env.Services, "plain", old); ok || err != nil {
		t.Fatalf("a stale delete applied: %v, %v", ok, err)
	}
	if got := raw(t, env, "plain"); got.Status != runs.Launching || got.LogTail != "" {
		t.Fatalf("state %+v", got)
	}
}

func TestFinishingATeardownRemovesTheState(t *testing.T) {
	env := fixture(t)
	launched(t, env, "plain", ann.Email)
	_, _ = runs.RequestTeardown(ctx, env.Services, "plain", ann)
	run := raw(t, env, "plain").RunID
	if ok, err := runs.FinishGone(ctx, env.Services, "plain", run); !ok || err != nil {
		t.Fatalf("%v, %v", ok, err)
	}
	if raw(t, env, "plain") != nil {
		t.Fatal("the state is still there")
	}
	states, err := runs.StatesFor(ctx, env.Services, []string{"plain", "pims"})
	if err != nil || len(states) != 0 {
		t.Fatalf("%v, %v", states, err)
	}
}

func TestResumesAndLogAreRecorded(t *testing.T) {
	env := fixture(t)
	_, _ = runs.RequestLaunch(ctx, env.Services, "plain", ann.Email)
	run := raw(t, env, "plain").RunID
	if ok, err := runs.UpdateLogAndResumes(ctx, env.Services, "plain", run, "tail", 3); !ok || err != nil {
		t.Fatalf("%v, %v", ok, err)
	}
	if got := raw(t, env, "plain"); got.LogTail != "tail" || got.Resumes != 3 || got.Status != runs.Launching {
		t.Fatalf("state %+v", got)
	}
}

func TestTheTeardownClearsTheLastError(t *testing.T) {
	env := fixture(t)
	run := launched(t, env, "plain", ann.Email)
	_, _ = runs.Finish(ctx, env.Services, "plain", run, runs.Running, runs.Outcome{Error: "old trouble"})
	if _, err := runs.RequestTeardown(ctx, env.Services, "plain", ann); err != nil {
		t.Fatal(err)
	}
	if got := raw(t, env, "plain"); got.Error != "" || got.Action != runs.Destroy || got.Resumes != 0 {
		t.Fatalf("state %+v", got)
	}
}

func TestThePublicViewHidesInternals(t *testing.T) {
	env := fixture(t)
	_, _ = runs.RequestLaunch(ctx, env.Services, "plain", ann.Email)
	pub := raw(t, env, "plain").Public()
	if pub.Status != "launching" || pub.LaunchedBy != ann.Email || pub.Ref != testenv.SHA {
		t.Fatalf("%+v", pub)
	}
	var none *runs.State
	if none.Public() != nil {
		t.Fatal("a missing state must have no public view")
	}
}
