// Package runs is a demo's deployment: requests to launch or tear it down, and their outcome.
//
// One item per demo holds its state. Every change is a conditional write, so two
// clicks, or a stale runner, cannot start or finish the wrong run. See the state
// machine in docs/specs/001_design.md.
package runs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"launchpad/internal/apperr"
	"launchpad/internal/catalog"
	"launchpad/internal/credentials"
	"launchpad/internal/db"
	"launchpad/internal/services"
	"launchpad/internal/users"
)

// Statuses of a demo.
const (
	Launching      = "launching"
	Running        = "running"
	LaunchFailed   = "launch_failed"
	Updating       = "updating"
	UpdateFailed   = "update_failed"
	TearingDown    = "tearing_down"
	TeardownFailed = "teardown_failed"
)

// Actions of a run.
const (
	Apply   = "apply"
	Destroy = "destroy"
)

// State is the item that holds a demo's deployment.
type State struct {
	PK             string `dynamodbav:"pk"`
	SK             string `dynamodbav:"sk"`
	Status         string `dynamodbav:"status"`
	Action         string `dynamodbav:"action"`
	RunID          string `dynamodbav:"runId"`
	Ref            string `dynamodbav:"ref"`
	Version        string `dynamodbav:"version,omitempty"`
	Path           string `dynamodbav:"path"`
	NeedsConfluent bool   `dynamodbav:"needsConfluent"`
	LaunchedBy     string `dynamodbav:"launchedBy"`
	CredsOwner     string `dynamodbav:"credsOwner"`
	// What was applied before an update began. Nothing of it is lost until the update starts to apply.
	PrevRef            string `dynamodbav:"prevRef,omitempty"`
	PrevVersion        string `dynamodbav:"prevVersion,omitempty"`
	PrevPath           string `dynamodbav:"prevPath,omitempty"`
	PrevNeedsConfluent bool   `dynamodbav:"prevNeedsConfluent,omitempty"`
	Resumes            int64  `dynamodbav:"resumes"`
	UpdatedAt          int64  `dynamodbav:"updatedAt"`
	URL                string `dynamodbav:"url,omitempty"`
	Error              string `dynamodbav:"error,omitempty"`
	LogTail            string `dynamodbav:"logTail,omitempty"`
}

// Applied is the code a demo was last applied at.
type Applied struct {
	Ref, Version, Path string
	NeedsConfluent     bool
}

// Applied is the code the state records now.
func (s *State) applied() *Applied {
	return &Applied{Ref: s.Ref, Version: s.Version, Path: s.Path, NeedsConfluent: s.NeedsConfluent}
}

// Previous is the code the demo was at before the update that is running or failed, or nil
// if there is none.
func (s *State) Previous() *Applied {
	if s.PrevRef == "" {
		return nil
	}
	return &Applied{Ref: s.PrevRef, Version: s.PrevVersion, Path: s.PrevPath, NeedsConfluent: s.PrevNeedsConfluent}
}

// Public is what the API shows of a State.
type Public struct {
	Status     string `json:"status"`
	URL        string `json:"url"`
	Error      string `json:"error"`
	LogTail    string `json:"logTail"`
	LaunchedBy string `json:"launchedBy"`
	UpdatedAt  int64  `json:"updatedAt"`
	Ref        string `json:"ref"`
	Version    string `json:"-"`
}

// Public is the view of a state that the API shows.
func (s *State) Public() *Public {
	if s == nil {
		return nil
	}
	return &Public{
		Status: s.Status, URL: s.URL, Error: s.Error, LogTail: s.LogTail,
		LaunchedBy: s.LaunchedBy, UpdatedAt: s.UpdatedAt, Ref: s.Ref, Version: s.Version,
	}
}

// Behind is whether a demo that was applied at this version and commit is older than the catalog's
// entry. It compares the demo's own version, so a commit that changes only other demos is not news. A
// demo applied before versions were recorded, or a catalog read before then, is compared by commit.
func Behind(version, ref string, demo *catalog.Demo) bool {
	if version != "" && demo.Version != "" {
		return version != demo.Version
	}
	return ref != demo.Ref
}

func key(demoID string) string { return "DEMO#" + demoID }

// GetRaw is a demo's state, or nil if it is not deployed.
func GetRaw(ctx context.Context, svc *services.Services, demoID string) (*State, error) {
	return db.GetAs[State](ctx, svc.DB, key(demoID), "STATE")
}

func newRunID() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("random run id: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

type runEvent struct {
	DemoID  string `json:"demoId"`
	RunID   string `json:"runId"`
	Resumes int    `json:"resumes"`
}

func invoke(ctx context.Context, svc *services.Services, demoID, runID string, resumes int) error {
	payload, err := json.Marshal(runEvent{DemoID: demoID, RunID: runID, Resumes: resumes})
	if err != nil {
		return err
	}
	_, err = svc.Lambda.Invoke(ctx, &lambda.InvokeInput{
		FunctionName:   &svc.Settings.RunnerFunction,
		InvocationType: lambdatypes.InvocationTypeEvent,
		Payload:        payload,
	})
	return err
}

// InvokeResume starts the next slice of a run that ran out of time.
func InvokeResume(ctx context.Context, svc *services.Services, demoID, runID string, resumes int) error {
	return invoke(ctx, svc, demoID, runID, resumes)
}

// RequestLaunch records a launch and starts the runner.
func RequestLaunch(ctx context.Context, svc *services.Services, demoID, email string) (*Public, error) {
	demo, err := catalog.Get(ctx, svc, demoID)
	if err != nil {
		return nil, err
	}
	if demo == nil {
		return nil, apperr.NotFound("No such demo")
	}
	if !demo.Valid {
		return nil, apperr.Conflict("invalid_demo", "This demo does not follow the Launchpad interface: "+strings.Join(demo.Problems, "; "))
	}
	if demo.NeedsConfluent {
		status, err := credentials.GetStatus(ctx, svc, email)
		if err != nil {
			return nil, err
		}
		if !status.Configured {
			return nil, apperr.Conflict("credentials_required", "Save your Confluent Cloud API key before launching this demo")
		}
	}

	runID, err := newRunID()
	if err != nil {
		return nil, err
	}
	err = db.PutAs(ctx, svc.DB, State{
		PK: key(demoID), SK: "STATE", Status: Launching, Action: Apply, RunID: runID,
		Ref: demo.Ref, Version: demo.Version, Path: demo.Path, NeedsConfluent: demo.NeedsConfluent,
		LaunchedBy: email, CredsOwner: email, Resumes: 0, UpdatedAt: svc.Now(),
	}, db.Or(db.Absent("pk"), db.Equal("status", LaunchFailed)))
	if errors.Is(err, db.ErrConditionFailed) {
		status := "busy"
		if current, getErr := GetRaw(ctx, svc, demoID); getErr == nil && current != nil {
			status = current.Status
		}
		return nil, apperr.Conflict("not_launchable", "The demo is "+status)
	}
	if err != nil {
		return nil, err
	}
	svc.Metrics.Count("DemoLaunchRequested", "demo", demoID)
	if err := start(ctx, svc, demoID, runID, LaunchFailed, Outcome{}); err != nil {
		return nil, err
	}
	current, err := GetRaw(ctx, svc, demoID)
	return current.Public(), err
}

// RequestTeardown records a teardown and starts the runner.
func RequestTeardown(ctx context.Context, svc *services.Services, demoID string, user *users.User) (*Public, error) {
	state, err := GetRaw(ctx, svc, demoID)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, apperr.NotFound("The demo is not deployed")
	}
	if user.Role != users.Admin && user.Email != state.LaunchedBy {
		return nil, apperr.Forbidden("Only the person who launched a demo, or an admin, can tear it down")
	}

	// Destroying must stay possible when the launcher's key is gone, so the requester's will do.
	owner, err := credsOwner(ctx, svc, state, []string{state.LaunchedBy, user.Email}, state.NeedsConfluent,
		"Save your Confluent Cloud API key before tearing this demo down")
	if err != nil {
		return nil, err
	}

	runID, err := newRunID()
	if err != nil {
		return nil, err
	}
	previous := state.Status
	_, err = svc.DB.Update(ctx, key(demoID), "STATE", db.Update{Set: map[string]any{
		"status": TearingDown, "action": Destroy, "runId": runID, "credsOwner": owner,
		"resumes": 0, "updatedAt": svc.Now(), "error": "",
	}}, db.And(
		db.Equal("runId", state.RunID),
		db.Equal("status", previous),
		db.OneOf("status", Running, LaunchFailed, UpdateFailed, TeardownFailed),
	))
	if errors.Is(err, db.ErrConditionFailed) {
		return nil, apperr.Conflict("not_tearable", fmt.Sprintf("The demo is %s and cannot be torn down now", previous))
	}
	if err != nil {
		return nil, err
	}
	svc.Metrics.Count("DemoTeardownRequested", "demo", demoID)
	if err := start(ctx, svc, demoID, runID, TeardownFailed, Outcome{}); err != nil {
		return nil, err
	}
	current, err := GetRaw(ctx, svc, demoID)
	return current.Public(), err
}

// credsOwner is whose Confluent Cloud key a run of a deployed demo uses: the first
// of the candidates who has saved one. Terraform needs the same Confluent Cloud
// account that created things. A demo that needs no key keeps the owner it has.
func credsOwner(ctx context.Context, svc *services.Services, state *State, candidates []string, needsConfluent bool, problem string) (string, error) {
	if !needsConfluent {
		return state.CredsOwner, nil
	}
	for _, candidate := range candidates {
		status, err := credentials.GetStatus(ctx, svc, candidate)
		if err != nil {
			return "", err
		}
		if status.Configured {
			return candidate, nil
		}
	}
	return "", apperr.Conflict("credentials_required", problem)
}

// RequestUpdate records an update of a deployed demo to the newest commit, and starts the runner.
//
// It re-reads the repository first, so a commit pushed a moment ago is the one
// that is applied, not the one the catalog cached up to CacheSeconds ago.
func RequestUpdate(ctx context.Context, svc *services.Services, demoID string, user *users.User) (*Public, error) {
	state, err := GetRaw(ctx, svc, demoID)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, apperr.NotFound("The demo is not deployed")
	}
	if user.Role != users.Admin && user.Email != state.LaunchedBy {
		return nil, apperr.Forbidden("Only the person who launched a demo, or an admin, can update it")
	}
	if state.Status != Running && state.Status != UpdateFailed {
		return nil, apperr.Conflict("not_updatable", fmt.Sprintf("The demo is %s and cannot be updated now", state.Status))
	}

	if _, err := catalog.List(ctx, svc, true); err != nil {
		// GitHub's own failures say what went wrong, and anything else is a 500 that says nothing.
		svc.Log.Error("Could not read the repository for an update", "demo", demoID, "error", err)
		return nil, err
	}
	demo, err := catalog.Get(ctx, svc, demoID)
	if err != nil {
		return nil, err
	}
	if demo == nil {
		return nil, apperr.Conflict("demo_removed", "The demo is no longer in the repository, so it can only be torn down")
	}
	if !demo.Valid {
		return nil, apperr.Conflict("invalid_demo", "This demo does not follow the Launchpad interface: "+strings.Join(demo.Problems, "; "))
	}
	if state.Status == Running && !Behind(state.Version, state.Ref, demo) {
		return nil, apperr.Conflict("up_to_date", "The demo is already at the latest commit")
	}
	// The resources that exist were made with the old code, and the new code may want a key too.
	needsConfluent := state.NeedsConfluent || demo.NeedsConfluent
	// An update creates and changes things, so only the key of the Confluent Cloud account that
	// made them will do. Another person's key would be pointed at resources that are not theirs.
	owner, err := credsOwner(ctx, svc, state, []string{state.LaunchedBy}, needsConfluent,
		"The person who launched this demo must save their Confluent Cloud API key before it can be updated")
	if err != nil {
		return nil, err
	}

	runID, err := newRunID()
	if err != nil {
		return nil, err
	}
	previous := state.Status
	_, err = svc.DB.Update(ctx, key(demoID), "STATE", db.Update{Set: map[string]any{
		"status": Updating, "action": Apply, "runId": runID, "ref": demo.Ref, "version": demo.Version, "path": demo.Path,
		"needsConfluent": needsConfluent, "credsOwner": owner, "resumes": 0,
		"prevRef": state.Ref, "prevVersion": state.Version, "prevPath": state.Path, "prevNeedsConfluent": state.NeedsConfluent,
		"updatedAt": svc.Now(), "error": "", "logTail": "",
	}}, db.And(
		db.Equal("runId", state.RunID),
		db.Equal("status", previous),
		db.OneOf("status", Running, UpdateFailed),
	))
	if errors.Is(err, db.ErrConditionFailed) {
		return nil, apperr.Conflict("not_updatable", "The demo changed while the update was being requested. Try again")
	}
	if err != nil {
		return nil, err
	}
	svc.Metrics.Count("DemoUpdateRequested", "demo", demoID)
	// If the runner cannot start, nothing was applied, so the demo is still at the code it was at.
	if err := start(ctx, svc, demoID, runID, UpdateFailed, Outcome{URL: state.URL, Restore: state.applied()}); err != nil {
		return nil, err
	}
	current, err := GetRaw(ctx, svc, demoID)
	return current.Public(), err
}

// start invokes the runner. If it cannot, the run ends in failedStatus with the outcome given and an error.
func start(ctx context.Context, svc *services.Services, demoID, runID, failedStatus string, outcome Outcome) error {
	err := invoke(ctx, svc, demoID, runID, 0)
	if err == nil {
		return nil
	}
	// The runner could not even be started.
	svc.Log.Error("Could not start the runner", "demo", demoID, "error", err)
	outcome.Error = fmt.Sprintf("Could not start the runner: %T", err)
	if _, finishErr := Finish(ctx, svc, demoID, runID, failedStatus, outcome); finishErr != nil {
		svc.Log.Error("Could not record that the runner did not start", "demo", demoID, "error", finishErr)
	}
	return apperr.New(502, "runner_unavailable", "The deployment worker could not be started. Try again")
}

// UpdateLog records the latest output. False means this run is no longer the current one.
func UpdateLog(ctx context.Context, svc *services.Services, demoID, runID, tail string) (bool, error) {
	return updateLog(ctx, svc, demoID, runID, map[string]any{"logTail": tail, "updatedAt": svc.Now()})
}

// UpdateLogAndResumes is UpdateLog that also records how many times the run has resumed.
func UpdateLogAndResumes(ctx context.Context, svc *services.Services, demoID, runID, tail string, resumes int) (bool, error) {
	return updateLog(ctx, svc, demoID, runID, map[string]any{"logTail": tail, "updatedAt": svc.Now(), "resumes": resumes})
}

func updateLog(ctx context.Context, svc *services.Services, demoID, runID string, set map[string]any) (bool, error) {
	_, err := svc.DB.Update(ctx, key(demoID), "STATE", db.Update{Set: set}, db.Equal("runId", runID))
	return applied(err)
}

// Outcome is what a run ended with.
type Outcome struct {
	URL   string
	Error string
	Tail  *string // nil leaves the log as it is
	// Restore puts the state back at this code. It is for an update that failed before it applied
	// anything, so that the demo, and a teardown of it, are still at what was applied.
	Restore *Applied
}

// Finish ends a run with a status. False means this run is no longer the current one.
func Finish(ctx context.Context, svc *services.Services, demoID, runID, status string, outcome Outcome) (bool, error) {
	set := map[string]any{"status": status, "url": outcome.URL, "error": outcome.Error, "updatedAt": svc.Now()}
	if outcome.Tail != nil {
		set["logTail"] = *outcome.Tail
	}
	if r := outcome.Restore; r != nil {
		set["ref"], set["version"], set["path"], set["needsConfluent"] = r.Ref, r.Version, r.Path, r.NeedsConfluent
	}
	_, err := svc.DB.Update(ctx, key(demoID), "STATE", db.Update{Set: set}, db.Equal("runId", runID))
	return applied(err)
}

// FinishGone ends a teardown that succeeded: the demo is no longer deployed.
func FinishGone(ctx context.Context, svc *services.Services, demoID, runID string) (bool, error) {
	return applied(svc.DB.Delete(ctx, key(demoID), "STATE", db.Equal("runId", runID)))
}

func applied(err error) (bool, error) {
	if errors.Is(err, db.ErrConditionFailed) {
		return false, nil
	}
	return err == nil, err
}

// StatesFor is the public state of each of these demos that is deployed, by demo id.
func StatesFor(ctx context.Context, svc *services.Services, demoIDs []string) (map[string]*Public, error) {
	out := map[string]*Public{}
	for _, id := range demoIDs {
		state, err := GetRaw(ctx, svc, id)
		if err != nil {
			return nil, err
		}
		if state != nil {
			out[id] = state.Public()
		}
	}
	return out, nil
}
