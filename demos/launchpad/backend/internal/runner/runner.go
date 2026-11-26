// Package runner applies or destroys one demo with Terraform.
//
// It is invoked asynchronously by the API. A Lambda cannot run longer than 15
// minutes and a demo can take longer than that to create, so a run that is not
// finished near the limit tells Terraform to stop, then invokes the runner again.
// Terraform picks up where the state says it was.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"launchpad/internal/credentials"
	"launchpad/internal/demoif"
	"launchpad/internal/runs"
	"launchpad/internal/services"
	"launchpad/internal/source"
	"launchpad/internal/terraform"
)

const (
	managedKey = "launchpad:managed"
	demoKey    = "launchpad:demo"

	// Results of a slice, for the logs and for tests.
	Stale     = "stale"
	Resumed   = "resumed"
	Failed    = "failed"
	Running   = "running"
	Destroyed = "destroyed"

	maxMessage = 1000
)

var lockID = regexp.MustCompile(`(?m)^\s*ID:\s+([0-9a-fA-F-]{36})\s*$`)

// passThrough is the environment that Terraform and its providers may see. Nothing else of the Lambda's is passed on.
var passThrough = []string{"PATH", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_REGION", "AWS_DEFAULT_REGION"}

// Event is the payload the runner is invoked with.
type Event struct {
	DemoID  string `json:"demoId"`
	RunID   string `json:"runId"`
	Resumes int    `json:"resumes"`
}

// Runner does slices of runs.
type Runner struct {
	Svc  *services.Services
	Work string // scratch directory: Terraform, its plugins and the demos' code
	// MaxResumes is how many times a run may continue in a new invocation.
	MaxResumes int
	// Reserve is left at the end of an invocation for Terraform to stop and for the result to be written.
	Reserve time.Duration
	// MinUseful is the least time left that is worth starting Terraform for.
	MinUseful time.Duration
	// Grace is how long Terraform gets to stop after an interrupt.
	Grace time.Duration
	// HTTP sends the shutdown request to a demo. Nil means a client that gives each request 65 seconds and follows no redirect.
	HTTP services.HTTP
	// ShutdownBackoff is the wait between tries of the shutdown request.
	ShutdownBackoff time.Duration
	// LookupEnv reads the Lambda's environment. It is os.LookupEnv unless a test replaces it.
	LookupEnv func(string) (string, bool)
}

// New returns a Runner with the production limits.
func New(svc *services.Services, work string) *Runner {
	return &Runner{
		Svc:        svc,
		Work:       work,
		MaxResumes: 8,
		Reserve:    terraform.StopGrace + 45*time.Second,
		MinUseful:  120 * time.Second,
		Grace:      terraform.StopGrace,
		LookupEnv:  os.LookupEnv,

		ShutdownBackoff: 5 * time.Second,
	}
}

// failure is an error whose text is shown to the person as it is.
type failure string

func (f failure) Error() string { return string(f) }

func backendArgs(svc *services.Services, demoID string) []string {
	return []string{
		"-backend-config=bucket=" + svc.Settings.StateBucket,
		"-backend-config=key=demos/" + demoID + "/terraform.tfstate",
		"-backend-config=region=" + svc.Settings.Region,
		"-backend-config=use_lockfile=true",
		"-backend-config=encrypt=true",
	}
}

// BuildEnv is the environment Terraform runs with.
func (r *Runner) BuildEnv(demoID string, creds *credentials.Credentials) (map[string]string, error) {
	env := map[string]string{}
	for _, k := range passThrough {
		if v, ok := r.LookupEnv(k); ok {
			env[k] = v
		}
	}
	tags, err := json.Marshal(map[string]string{managedKey: "true", demoKey: demoID})
	if err != nil {
		return nil, err
	}
	for k, v := range map[string]string{
		"HOME":                     filepath.Join(r.Work, "home"),
		"TF_IN_AUTOMATION":         "1",
		"TF_INPUT":                 "0",
		"TF_CLI_ARGS":              "-no-color",
		"TF_DATA_DIR":              filepath.Join(r.Work, "data", demoID),
		"TF_PLUGIN_CACHE_DIR":      filepath.Join(r.Work, "plugin-cache"),
		"TF_VAR_aws_region":        r.Svc.Settings.Region,
		"TF_VAR_launchpad_demo_id": demoID,
		"TF_VAR_launchpad_tags":    string(tags),
	} {
		env[k] = v
	}
	if creds != nil {
		for k, v := range map[string]string{
			"TF_VAR_confluent_cloud_api_key":    creds.APIKey,
			"TF_VAR_confluent_cloud_api_secret": creds.APISecret,
			// The Confluent provider reads these itself.
			"CONFLUENT_CLOUD_API_KEY":    creds.APIKey,
			"CONFLUENT_CLOUD_API_SECRET": creds.APISecret,
		} {
			env[k] = v
		}
	}
	return env, nil
}

func environ(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// Run does one slice of a run and says what happened. An error means the
// state of the run could not even be read or written: the run is then left as
// it was.
func (r *Runner) Run(ctx context.Context, ev Event, remaining func() time.Duration) (string, error) {
	svc := r.Svc
	state, err := runs.GetRaw(ctx, svc, ev.DemoID)
	if err != nil {
		return "", err
	}
	if state == nil || state.RunID != ev.RunID || (state.Status != runs.Launching && state.Status != runs.Updating && state.Status != runs.TearingDown) {
		svc.Log.Warn("Ignoring a runner invocation for a run that is no longer current", "demo", ev.DemoID)
		return Stale, nil
	}

	a := &slice{Runner: r, ev: ev, state: state, remaining: remaining, started: time.Now(), tail: terraform.NewLogTail()}
	a.destroy = state.Action == runs.Destroy
	a.update = state.Status == runs.Updating
	a.verb, a.failedStatus, a.failedMetric = "apply", runs.LaunchFailed, "DemoLaunchFailed"
	a.doneMetric = "DemoLaunchSucceeded"
	if a.update {
		a.failedStatus, a.failedMetric, a.doneMetric = runs.UpdateFailed, "DemoUpdateFailed", "DemoUpdateSucceeded"
	}
	if a.destroy {
		a.verb, a.failedStatus, a.failedMetric = "destroy", runs.TeardownFailed, "DemoTeardownFailed"
	}

	result, err := a.do(ctx)
	if err == nil {
		return result, nil
	}
	svc.Log.Error("Run failed", "demo", ev.DemoID, "error", err)
	message := err.Error()
	if len(message) > maxMessage {
		message = strings.ToValidUTF8(message[:maxMessage], "")
	}
	text := a.tail.Text()
	outcome := runs.Outcome{Error: message, Tail: &text}
	if a.update {
		// The demo is probably still there, so keep the address it had.
		outcome.URL = state.URL
	}
	if _, ferr := runs.Finish(ctx, svc, ev.DemoID, ev.RunID, a.failedStatus, outcome); ferr != nil {
		return "", ferr
	}
	svc.Metrics.Count(a.failedMetric, "demo", ev.DemoID)
	return Failed, nil
}

// slice is one invocation's share of a run.
type slice struct {
	*Runner
	ev        Event
	state     *runs.State
	remaining func() time.Duration
	started   time.Time
	tail      *terraform.LogTail // replaced by one that hides the credentials, once they are loaded

	destroy      bool
	update       bool   // an apply of a newer commit to a demo that was running
	doneMetric   string // counted when an apply succeeds
	verb         string // the Terraform command
	failedStatus string
	failedMetric string
}

// do the work of the slice. Any error it returns fails the run.
func (a *slice) do(ctx context.Context) (string, error) {
	svc, ev, state := a.Svc, a.ev, a.state
	var creds *credentials.Credentials
	if state.NeedsConfluent {
		var err error
		creds, err = credentials.Load(ctx, svc, state.CredsOwner)
		if err != nil {
			return "", err
		}
		if creds == nil {
			return "", failure("The saved Confluent Cloud credentials are gone. Save them again and retry")
		}
		a.tail = terraform.NewLogTail(creds.APISecret, creds.APIKey)
	}
	log := a.tail
	action := "applying"
	if a.destroy {
		action = "destroying"
	}
	if a.update {
		action = "updating"
	}
	ref := state.Ref
	if len(ref) > 12 {
		ref = ref[:12]
	}
	log.Add(fmt.Sprintf("Launchpad: %s %s at %s (slice %d)", action, ev.DemoID, ref, ev.Resumes+1))

	// Terraform fails on a plugin cache or data directory that does not exist.
	for _, sub := range []string{"home", "plugin-cache", filepath.Join("data", ev.DemoID)} {
		if err := os.MkdirAll(filepath.Join(a.Work, sub), 0o755); err != nil {
			return "", err
		}
	}
	demoDir, err := source.Download(ctx, svc, state.Ref, state.Path, filepath.Join(a.Work, "src", ev.DemoID))
	if err != nil {
		return "", err
	}
	files, err := source.ReadTF(demoDir)
	if err != nil {
		return "", err
	}
	if problems := demoif.Check(files).Problems; len(problems) > 0 {
		return "", failure("The demo does not follow the Launchpad interface: " + strings.Join(problems, "; "))
	}
	tfDir := filepath.Join(demoDir, "tf")
	if err := os.WriteFile(filepath.Join(tfDir, "zz_launchpad_backend.tf"), []byte("terraform {\n  backend \"s3\" {}\n}\n"), 0o644); err != nil {
		return "", err
	}

	binary, err := terraform.EnsureBinary(ctx, svc, filepath.Join(a.Work, "bin"))
	if err != nil {
		return "", err
	}
	env, err := a.BuildEnv(ev.DemoID, creds)
	if err != nil {
		return "", err
	}
	onTick := func() {
		if _, err := runs.UpdateLog(ctx, svc, ev.DemoID, ev.RunID, log.Text()); err != nil {
			svc.Log.Warn("Could not record progress", "demo", ev.DemoID, "error", err)
		}
	}
	tf := terraform.New(binary, tfDir, environ(env), log, onTick)
	tf.Grace = a.Grace

	initArgs := append([]string{"init", "-input=false", "-reconfigure"}, backendArgs(svc, ev.DemoID)...)
	init, err := tf.Run(ctx, time.Time{}, initArgs...)
	if err != nil {
		return "", err
	}
	if init.Code != 0 {
		return "", failure("terraform init failed")
	}

	// A demo may need to clean up what it set up outside Terraform before Terraform deletes
	// what it made. Later slices of the same teardown do not ask again: the demo is
	// already shut down, or partly destroyed.
	if a.destroy && ev.Resumes == 0 {
		if err := a.shutDownDemo(ctx, tf, creds); err != nil {
			return "", err
		}
	}

	budget := a.remaining() - a.Reserve
	if budget < a.MinUseful {
		return a.resume(ctx, "not enough time left")
	}
	deadline := time.Now().Add(budget)

	args := []string{a.verb, "-auto-approve", "-input=false", "-lock-timeout=60s"}
	result, err := tf.Run(ctx, deadline, args...)
	if err != nil {
		return "", err
	}
	if result.Code != 0 && !result.Interrupted && strings.Contains(result.Output, "Error acquiring the state lock") {
		// A previous slice was cut off before it released the lock. The run id
		// proves no other run of this demo is active, so the lock is stale.
		if m := lockID.FindStringSubmatch(result.Output); m != nil {
			log.Add("Launchpad: clearing a stale state lock left by an earlier slice")
			if _, err := tf.Run(ctx, time.Time{}, "force-unlock", "-force", m[1]); err != nil {
				return "", err
			}
			if result, err = tf.Run(ctx, deadline, args...); err != nil {
				return "", err
			}
		}
	}
	if result.Interrupted && result.Code != 0 {
		return a.resume(ctx, "ran out of time")
	}
	if result.Code != 0 {
		return "", failure("terraform " + a.verb + " failed")
	}

	url := ""
	if !a.destroy {
		if url, err = readURL(ctx, tf, creds); err != nil {
			return "", err
		}
		if url != "" {
			log.Add(url)
		}
	}
	svc.Metrics.Emit("TerraformSeconds", time.Since(a.started).Seconds(), "Seconds", "action", a.verb)
	if a.destroy {
		if _, err := runs.FinishGone(ctx, svc, ev.DemoID, ev.RunID); err != nil {
			return "", err
		}
		return Destroyed, nil
	}
	text := log.Text()
	if _, err := runs.Finish(ctx, svc, ev.DemoID, ev.RunID, runs.Running, runs.Outcome{URL: url, Tail: &text}); err != nil {
		return "", err
	}
	svc.Metrics.Count(a.doneMetric, "demo", ev.DemoID)
	return Running, nil
}

// readURL reads the demo's launchpad_url output. An output that cannot be read, or is empty, is an empty address.
func readURL(ctx context.Context, tf *terraform.Terraform, creds *credentials.Credentials) (string, error) {
	return readOutput(ctx, tf, creds, "launchpad_url", true)
}

// readOutput reads one of the demo's outputs with `output -raw`. One that cannot be read,
// because the demo has none or nothing was applied, is empty. If logFailure is set, what
// Terraform said about that goes to the run's log.
func readOutput(ctx context.Context, tf *terraform.Terraform, creds *credentials.Credentials, name string, logFailure bool) (string, error) {
	// The command gets a log of its own, so what it prints is the answer and nothing before it.
	only := *tf
	if creds != nil {
		only.Log = terraform.NewLogTail(creds.APISecret, creds.APIKey)
	} else {
		only.Log = terraform.NewLogTail()
	}
	out, err := only.Run(ctx, time.Time{}, "output", "-raw", name)
	if err != nil {
		return "", err
	}
	if out.Code != 0 {
		if logFailure {
			for _, line := range strings.Split(out.Output, "\n") {
				tf.Log.Add(line)
			}
		}
		return "", nil
	}
	lines := strings.Split(strings.TrimSpace(out.Output), "\n")
	return strings.TrimSpace(lines[len(lines)-1]), nil
}

func (a *slice) resume(ctx context.Context, why string) (string, error) {
	svc, ev := a.Svc, a.ev
	if ev.Resumes >= a.MaxResumes {
		return "", failure(fmt.Sprintf("Not finished after %d attempts", a.MaxResumes+1))
	}
	a.tail.Add(fmt.Sprintf("Launchpad: %s, continuing in a new invocation", why))
	current, err := runs.UpdateLogAndResumes(ctx, svc, ev.DemoID, ev.RunID, a.tail.Text(), ev.Resumes+1)
	if err != nil {
		return "", err
	}
	if !current {
		return Stale, nil
	}
	if err := runs.InvokeResume(ctx, svc, ev.DemoID, ev.RunID, ev.Resumes+1); err != nil {
		return "", fmt.Errorf("continue the run: %w", err)
	}
	return Resumed, nil
}
