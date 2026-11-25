package github_test

import (
	"context"
	"strings"
	"testing"

	"launchpad/internal/apperr"
	"launchpad/internal/github"
	"launchpad/internal/testenv"
)

var ctx = context.Background()

func wantAPIError(t *testing.T, err error, code string) {
	t.Helper()
	appErr, ok := apperr.As(err)
	if !ok || appErr.Status != 502 || appErr.Code != code {
		t.Fatalf("got %v, want 502 %s", err, code)
	}
}

func TestTheTokenGoesOnlyToTheAPIHost(t *testing.T) {
	env := testenv.New(t)
	env.Settings.GitHubToken = "ghp_secret"
	env.GitHub.AddRepo(map[string]string{"demos/a/tf/main.tf": testenv.GoodTF})
	env.GitHub.Tarball(nil)

	if _, err := github.ResolveRef(ctx, env.Services); err != nil {
		t.Fatal(err)
	}
	if _, err := github.Raw(ctx, env.Services, testenv.SHA, "demos/a/tf/main.tf"); err != nil {
		t.Fatal(err)
	}
	rc, err := github.Tarball(ctx, env.Services, testenv.SHA)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	for url, headers := range env.GitHub.Headers {
		got := headers.Get("Authorization")
		if strings.HasPrefix(url, "https://api.github.com/") && got != "Bearer ghp_secret" {
			t.Errorf("%s: authorization %q", url, got)
		}
		if !strings.HasPrefix(url, "https://api.github.com/") && got != "" {
			t.Errorf("%s: the token was sent to another host", url)
		}
		if headers.Get("User-Agent") != "launchpad" {
			t.Errorf("%s: user agent %q", url, headers.Get("User-Agent"))
		}
	}
	if got := env.GitHub.Headers["https://api.github.com/repos/owner/repo/commits/main"].Get("Accept"); got != "application/vnd.github.sha" {
		t.Errorf("accept %q", got)
	}
}

func TestNoTokenMeansNoAuthorizationHeader(t *testing.T) {
	env := testenv.New(t)
	env.GitHub.AddRepo(map[string]string{"demos/a/tf/main.tf": testenv.GoodTF})
	if _, err := github.ResolveRef(ctx, env.Services); err != nil {
		t.Fatal(err)
	}
	if got := env.GitHub.Headers["https://api.github.com/repos/owner/repo/commits/main"].Get("Authorization"); got != "" {
		t.Fatalf("authorization %q", got)
	}
}

func TestResolveRefChecksWhatItGets(t *testing.T) {
	env := testenv.New(t)
	env.GitHub.Route("https://api.github.com/repos/owner/repo/commits/main", []byte("not a sha\n"))
	_, err := github.ResolveRef(ctx, env.Services)
	wantAPIError(t, err, "github_error")

	env.GitHub.Route("https://api.github.com/repos/owner/repo/commits/main", []byte(testenv.SHA+"\n"))
	sha, err := github.ResolveRef(ctx, env.Services)
	if err != nil || sha != testenv.SHA {
		t.Fatalf("%q, %v", sha, err)
	}
}

func TestAMissingRepositoryIsNotFound(t *testing.T) {
	env := testenv.New(t)
	_, err := github.ResolveRef(ctx, env.Services)
	wantAPIError(t, err, "github_not_found")
}

func TestARepositoryOrRefThatCouldInjectAPathIsRefused(t *testing.T) {
	for _, repo := range []string{"", "owner", "owner/repo/extra", "../x/y", "owner/re po"} {
		env := testenv.New(t)
		env.Settings.DemosRepo = repo
		if _, err := github.ResolveRef(ctx, env.Services); err == nil {
			t.Errorf("repository %q was accepted", repo)
		}
		if len(env.GitHub.Calls()) != 0 {
			t.Errorf("repository %q reached GitHub", repo)
		}
	}
	for _, ref := range []string{"", "a b", "a?b", "a#b", strings.Repeat("a", 201)} {
		env := testenv.New(t)
		env.Settings.DemosRef = ref
		if _, err := github.ResolveRef(ctx, env.Services); err == nil {
			t.Errorf("ref %q was accepted", ref)
		}
	}
}

func TestTreeRefusesATruncatedOrEmptyAnswer(t *testing.T) {
	env := testenv.New(t)
	url := "https://api.github.com/repos/owner/repo/git/trees/" + testenv.SHA + "?recursive=1"

	env.GitHub.Route(url, []byte(`{"tree":[],"truncated":true}`))
	_, err := github.Tree(ctx, env.Services, testenv.SHA)
	wantAPIError(t, err, "github_truncated")

	// An answer with no tree at all must not read as "no demos", which would empty the catalog.
	env.GitHub.Route(url, []byte(`{"message":"Bad credentials"}`))
	if _, err := github.Tree(ctx, env.Services, testenv.SHA); err == nil {
		t.Fatal("an answer without a tree was accepted")
	}
	env.GitHub.Route(url, []byte(`not json`))
	if _, err := github.Tree(ctx, env.Services, testenv.SHA); err == nil {
		t.Fatal("an answer that is not JSON was accepted")
	}

	env.GitHub.Route(url, []byte(`{"tree":[{"path":"demos","type":"tree"},{"path":"demos/a/main.tf","type":"blob"}],"truncated":false}`))
	entries, err := github.Tree(ctx, env.Services, testenv.SHA)
	if err != nil || len(entries) != 2 || entries[1].Path != "demos/a/main.tf" || entries[1].Type != "blob" {
		t.Fatalf("%+v, %v", entries, err)
	}
}

func TestRawKeepsTextAndReplacesBrokenBytes(t *testing.T) {
	env := testenv.New(t)
	env.GitHub.Route("https://raw.githubusercontent.com/owner/repo/"+testenv.SHA+"/f.tf", []byte("ok \xff end"))
	got, err := github.Raw(ctx, env.Services, testenv.SHA, "f.tf")
	if err != nil || got != "ok � end" {
		t.Fatalf("%q, %v", got, err)
	}
}
