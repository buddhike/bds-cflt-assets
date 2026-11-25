package source_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"launchpad/internal/apperr"
	"launchpad/internal/source"
	"launchpad/internal/testenv"
)

var ctx = context.Background()

var top = "repo-" + testenv.SHA + "/"

func serve(env *testenv.Env, members ...testenv.Member) {
	env.GitHub.Tarball(members)
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestExtractsOnlyTheDemo(t *testing.T) {
	env := testenv.New(t)
	serve(env,
		testenv.Member{Name: top, Kind: "dir"},
		testenv.Member{Name: top + "README.md", Data: "x"},
		testenv.Member{Name: top + "demos/pims/tf/main.tf", Data: "resource"},
		testenv.Member{Name: top + "demos/pims/tf/run.sh", Data: "#!/bin/sh", Kind: "exec"},
		testenv.Member{Name: top + "demos/other/tf/main.tf", Data: "nope"},
		testenv.Member{Name: top + "demos/pimsx/tf/main.tf", Data: "nope"},
	)
	dest := filepath.Join(t.TempDir(), "w")
	out, err := source.Download(ctx, env.Services, testenv.SHA, "demos/pims", dest)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(out, "tf/main.tf")); got != "resource" {
		t.Fatalf("main.tf is %q", got)
	}
	for _, name := range []string{"README.md", "other", "x", "pimsx"} {
		if exists(filepath.Join(out, name)) {
			t.Errorf("%s was extracted", name)
		}
	}
	if info, _ := os.Stat(filepath.Join(out, "tf/run.sh")); info == nil || info.Mode()&0o111 == 0 {
		t.Error("an executable file lost its mode")
	}
	if info, _ := os.Stat(filepath.Join(out, "tf/main.tf")); info == nil || info.Mode()&0o111 != 0 {
		t.Error("a plain file became executable")
	}
	files, err := source.ReadTF(out)
	if err != nil || len(files) != 1 || files["main.tf"] != "resource" {
		t.Fatalf("ReadTF: %v, %v", files, err)
	}
}

func TestAHostileArchiveCannotWriteOutside(t *testing.T) {
	env := testenv.New(t)
	serve(env,
		testenv.Member{Name: top + "demos/pims/tf/main.tf", Data: "ok"},
		testenv.Member{Name: top + "demos/pims/../../../evil.txt", Data: "evil"},
		testenv.Member{Name: top + "demos/pims/tf/link", Data: "/etc/passwd", Kind: "symlink"},
		testenv.Member{Name: top + "demos/pims//abs", Data: "x"},
	)
	parent := t.TempDir()
	out, err := source.Download(ctx, env.Services, testenv.SHA, "demos/pims", filepath.Join(parent, "w"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(parent, "evil.txt"), filepath.Join(filepath.Dir(parent), "evil.txt"), filepath.Join(out, "tf/link"), filepath.Join(out, "abs"), "/abs"} {
		if exists(p) {
			t.Errorf("%s was written", p)
		}
	}
	if !exists(filepath.Join(out, "tf/main.tf")) {
		t.Error("the good file is missing")
	}
}

func TestAMissingTFDirectoryIsAnError(t *testing.T) {
	env := testenv.New(t)
	serve(env, testenv.Member{Name: top + "demos/pims/main.go", Data: "x"})
	_, err := source.Download(ctx, env.Services, testenv.SHA, "demos/pims", filepath.Join(t.TempDir(), "w"))
	if appErr, ok := apperr.As(err); !ok || appErr.Code != "demo_missing" {
		t.Fatalf("got %v, want demo_missing", err)
	}
}

func TestDownloadReplacesAPreviousCopy(t *testing.T) {
	env := testenv.New(t)
	serve(env, testenv.Member{Name: top + "demos/pims/tf/new.tf", Data: "n"})
	dest := filepath.Join(t.TempDir(), "w")
	if err := os.MkdirAll(filepath.Join(dest, "tf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "tf/old.tf"), []byte("o"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Download(ctx, env.Services, testenv.SHA, "demos/pims", dest); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dest, "tf/old.tf")) || !exists(filepath.Join(dest, "tf/new.tf")) {
		t.Fatal("the old copy was not replaced")
	}
}

func TestAnArchiveThatIsNotGzipIsAnError(t *testing.T) {
	env := testenv.New(t)
	env.GitHub.Route("https://codeload.github.com/owner/repo/tar.gz/"+testenv.SHA, []byte("not an archive"))
	if _, err := source.Download(ctx, env.Services, testenv.SHA, "demos/pims", filepath.Join(t.TempDir(), "w")); err == nil {
		t.Fatal("want an error")
	}
}

func TestAMissingArchiveIsABadGateway(t *testing.T) {
	env := testenv.New(t)
	_, err := source.Download(ctx, env.Services, testenv.SHA, "demos/pims", filepath.Join(t.TempDir(), "w"))
	if appErr, ok := apperr.As(err); !ok || appErr.Status != 502 {
		t.Fatalf("got %v, want a 502", err)
	}
}
