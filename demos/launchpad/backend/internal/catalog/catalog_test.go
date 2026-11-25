package catalog_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"launchpad/internal/apperr"
	"launchpad/internal/catalog"
	"launchpad/internal/testenv"
)

var ctx = context.Background()

var files = map[string]string{
	"demos/pims/tf/main.tf":        testenv.CloudTF,
	"demos/pims/demo.json":         `{"name": "PIMS", "description": "Clinic CDC demo"}`,
	"demos/pims/main.go":           "package main",
	"demos/plain/tf/main.tf":       testenv.GoodTF,
	"demos/broken/tf/main.tf":      `resource "x" "y" {}`,
	"demos/launchpad/backend/x.go": "",                         // not a demo: no tf directory
	"demos/Bad_Name/tf/main.tf":    testenv.GoodTF,             // not a valid id
	"demos/pims/tf/modules/m.tf":   `variable "aws_region" {}`, // nested, ignored
	"README.md":                    "",
}

func fixture(t *testing.T) *testenv.Env {
	env := testenv.New(t)
	env.GitHub.AddRepo(files)
	return env
}

func ids(demos []catalog.Demo) []string {
	var out []string
	for _, d := range demos {
		out = append(out, d.ID)
	}
	return out
}

func TestRefreshDiscoversDemosWithTF(t *testing.T) {
	env := fixture(t)
	demos, err := catalog.Refresh(ctx, env.Services)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(demos); !slices.Equal(got, []string{"broken", "pims", "plain"}) {
		t.Fatalf("demos %v", got)
	}
	by := map[string]catalog.Demo{}
	for _, d := range demos {
		by[d.ID] = d
	}
	pims, plain, broken := by["pims"], by["plain"], by["broken"]
	if pims.Name != "PIMS" || pims.Description != "Clinic CDC demo" || !pims.NeedsConfluent || !pims.Valid {
		t.Errorf("pims %+v", pims)
	}
	if plain.Name != "plain" || plain.NeedsConfluent {
		t.Errorf("plain %+v", plain)
	}
	if broken.Valid || len(broken.Problems) == 0 {
		t.Errorf("broken %+v", broken)
	}
	if pims.Ref != testenv.SHA || pims.Path != "demos/pims" {
		t.Errorf("pims %+v", pims)
	}
}

func TestADemoStoresAndReadsBackLikeItWasWritten(t *testing.T) {
	env := fixture(t)
	if _, err := catalog.Refresh(ctx, env.Services); err != nil {
		t.Fatal(err)
	}
	got, err := catalog.Get(ctx, env.Services, "plain")
	if err != nil || got == nil {
		t.Fatalf("%v, %v", got, err)
	}
	if got.Problems == nil || len(got.Problems) != 0 || !got.Valid || got.Path != "demos/plain" {
		t.Fatalf("%+v", got)
	}
	// The item is read by older and newer code alike, so its attribute names are fixed.
	item, _ := env.Table.Get(ctx, "CATALOG", "DEMO#plain")
	for _, attr := range []string{"pk", "sk", "id", "name", "description", "path", "ref", "valid", "problems", "needsConfluent"} {
		if item[attr] == nil {
			t.Errorf("attribute %q is not stored", attr)
		}
	}
}

func TestDemosPathIsConfigurable(t *testing.T) {
	env := fixture(t)
	env.GitHub.AddRepo(map[string]string{"launchpad-demos/x/tf/main.tf": testenv.GoodTF})
	env.Settings.DemosPath = "launchpad-demos"
	demos, err := catalog.Refresh(ctx, env.Services)
	if err != nil || !slices.Equal(ids(demos), []string{"x"}) {
		t.Fatalf("%v, %v", ids(demos), err)
	}
}

func TestRemovedDemosLeaveTheCatalog(t *testing.T) {
	env := fixture(t)
	_, _ = catalog.Refresh(ctx, env.Services)
	env.GitHub.AddRepo(map[string]string{"demos/plain/tf/main.tf": testenv.GoodTF})
	demos, err := catalog.Refresh(ctx, env.Services)
	if err != nil || !slices.Equal(ids(demos), []string{"plain"}) {
		t.Fatalf("%v, %v", ids(demos), err)
	}
	if got, _ := catalog.Get(ctx, env.Services, "pims"); got != nil {
		t.Fatal("pims is still in the catalog")
	}
}

func TestListUsesTheCacheUntilItIsStale(t *testing.T) {
	env := fixture(t)
	_, _ = catalog.List(ctx, env.Services, false)
	calls := len(env.GitHub.Calls())
	_, _ = catalog.List(ctx, env.Services, false)
	if got := len(env.GitHub.Calls()); got != calls {
		t.Fatalf("a fresh cache made %d more calls", got-calls)
	}
	env.Advance(catalog.CacheSeconds * time.Second)
	_, _ = catalog.List(ctx, env.Services, false)
	if got := len(env.GitHub.Calls()); got <= calls {
		t.Fatal("a stale cache was not refreshed")
	}
}

func TestAStaleCacheSurvivesAGitHubOutage(t *testing.T) {
	env := fixture(t)
	_, _ = catalog.List(ctx, env.Services, false)
	env.Advance(catalog.CacheSeconds * time.Second)
	env.GitHub.Clear()
	demos, err := catalog.List(ctx, env.Services, false)
	if err != nil || !slices.Equal(ids(demos), []string{"broken", "pims", "plain"}) {
		t.Fatalf("%v, %v", ids(demos), err)
	}
	if _, err := catalog.List(ctx, env.Services, true); err == nil {
		t.Fatal("a forced refresh must report the outage")
	}
}

func TestTheFirstLoadWithGitHubDownIsAnError(t *testing.T) {
	env := fixture(t)
	env.GitHub.Clear()
	_, err := catalog.List(ctx, env.Services, false)
	if appErr, ok := apperr.As(err); !ok || appErr.Status != 502 {
		t.Fatalf("got %v, want a 502", err)
	}
}

func TestGetRejectsIdsThatAreNotIds(t *testing.T) {
	env := fixture(t)
	_, _ = catalog.Refresh(ctx, env.Services)
	if got, _ := catalog.Get(ctx, env.Services, "pims"); got == nil || got.ID != "pims" {
		t.Fatalf("pims: %+v", got)
	}
	for _, id := range []string{"../pims", "PIMS", "", "a/b"} {
		if got, err := catalog.Get(ctx, env.Services, id); got != nil || err != nil {
			t.Errorf("Get(%q) = %+v, %v", id, got, err)
		}
	}
}

func TestGetLoadsTheCatalogWhenItWasNeverFetched(t *testing.T) {
	env := fixture(t)
	if got, _ := catalog.Get(ctx, env.Services, "pims"); got == nil || got.ID != "pims" {
		t.Fatalf("%+v", got)
	}
}

func TestADemoJSONThatIsNotAnObjectIsIgnored(t *testing.T) {
	env := testenv.New(t)
	env.GitHub.AddRepo(map[string]string{
		"demos/a/tf/main.tf": testenv.GoodTF, "demos/a/demo.json": `["not", "an", "object"]`,
		"demos/b/tf/main.tf": testenv.GoodTF, "demos/b/demo.json": `{not json`,
		"demos/c/tf/main.tf": testenv.GoodTF, "demos/c/demo.json": `{"name": 7, "description": null}`,
	})
	demos, err := catalog.Refresh(ctx, env.Services)
	if err != nil || len(demos) != 3 {
		t.Fatalf("%v, %v", demos, err)
	}
	for _, d := range demos {
		if d.Name != d.ID || d.Description != "" {
			t.Errorf("%s: %+v", d.ID, d)
		}
	}
}

func TestLongNamesAndDescriptionsAreCut(t *testing.T) {
	env := testenv.New(t)
	long, _ := json.Marshal(map[string]string{"name": string(make([]rune, 0)) + repeat("é", 200), "description": repeat("d", 900)})
	env.GitHub.AddRepo(map[string]string{"demos/a/tf/main.tf": testenv.GoodTF, "demos/a/demo.json": string(long)})
	demos, _ := catalog.Refresh(ctx, env.Services)
	if n, d := len([]rune(demos[0].Name)), len(demos[0].Description); n != 80 || d != 500 {
		t.Fatalf("name %d, description %d", n, d)
	}
}

func repeat(s string, n int) string {
	out := ""
	for range n {
		out += s
	}
	return out
}
