package hostenv

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func existing(dirs ...string) func(string) bool {
	set := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		set[d] = true
	}
	return func(p string) bool { return set[p] }
}

func TestExtendPath(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		dirs      []string
		exists    func(string) bool
		want      string
		wantAdded []string
	}{
		{
			name:      "launchd's minimal path gains the tool directories that exist",
			path:      "/usr/bin:/bin:/usr/sbin:/sbin",
			dirs:      []string{"/usr/local/bin", "/opt/homebrew/bin", "/nope"},
			exists:    existing("/usr/local/bin", "/opt/homebrew/bin"),
			want:      "/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin:/opt/homebrew/bin",
			wantAdded: []string{"/usr/local/bin", "/opt/homebrew/bin"},
		},
		{
			name:   "directories already on the path are left where they are",
			path:   "/opt/homebrew/bin:/usr/bin:/usr/local/bin/",
			dirs:   []string{"/usr/local/bin", "/opt/homebrew/bin"},
			exists: existing("/usr/local/bin", "/opt/homebrew/bin"),
			want:   "/opt/homebrew/bin:/usr/bin:/usr/local/bin/",
		},
		{
			name:      "a directory listed twice is added once",
			path:      "/usr/bin",
			dirs:      []string{"/usr/local/bin", "/usr/local/bin/"},
			exists:    existing("/usr/local/bin"),
			want:      "/usr/bin:/usr/local/bin",
			wantAdded: []string{"/usr/local/bin"},
		},
		{
			name:      "an empty path becomes just the added directories",
			path:      "",
			dirs:      []string{"/usr/local/bin"},
			exists:    existing("/usr/local/bin"),
			want:      "/usr/local/bin",
			wantAdded: []string{"/usr/local/bin"},
		},
		{
			name:      "an unclean entry already on the path counts as present",
			path:      "/usr/bin:/usr/local//bin",
			dirs:      []string{"/usr/local/bin", "/opt/homebrew/bin"},
			exists:    existing("/usr/local/bin", "/opt/homebrew/bin"),
			want:      "/usr/bin:/usr/local//bin:/opt/homebrew/bin",
			wantAdded: []string{"/opt/homebrew/bin"},
		},
		{
			name:      "a trailing colon is kept and the directories follow it",
			path:      "/usr/bin:",
			dirs:      []string{"/usr/local/bin"},
			exists:    existing("/usr/local/bin"),
			want:      "/usr/bin::/usr/local/bin",
			wantAdded: []string{"/usr/local/bin"},
		},
		{
			name:   "nothing existing means nothing changes",
			path:   "/usr/bin",
			dirs:   []string{"/usr/local/bin"},
			exists: existing(),
			want:   "/usr/bin",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, added := extendPath(tt.path, tt.dirs, tt.exists)
			if got != tt.want {
				t.Errorf("path = %q, want %q", got, tt.want)
			}
			if !reflect.DeepEqual(added, tt.wantAdded) {
				t.Errorf("added = %q, want %q", added, tt.wantAdded)
			}
		})
	}
}

func TestToolDirsCoverDockerDesktop(t *testing.T) {
	dirs := toolDirs("/Users/op")
	for _, want := range []string{
		"/usr/local/bin",
		"/Applications/Docker.app/Contents/Resources/bin",
		"/Users/op/.docker/bin",
	} {
		if !slices.Contains(dirs, want) {
			t.Errorf("toolDirs missing %q: %q", want, dirs)
		}
	}
}

func TestToolDirsIgnoreAHomeThatIsNotAbsolute(t *testing.T) {
	for _, home := range []string{"", "relative/home", "."} {
		for _, d := range toolDirs(home) {
			if !filepath.IsAbs(d) {
				t.Errorf("toolDirs(%q) has relative entry %q", home, d)
			}
		}
	}
}

func TestEnsureToolPathAppendsOnDarwin(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	exists := existing("/usr/local/bin", "/Users/op/.docker/bin")

	added, err := ensureToolPath("darwin", "/Users/op", exists)
	if err != nil {
		t.Fatalf("ensureToolPath() error = %v", err)
	}

	wantAdded := []string{"/usr/local/bin", "/Users/op/.docker/bin"}
	if !reflect.DeepEqual(added, wantAdded) {
		t.Errorf("added = %q, want %q", added, wantAdded)
	}
	if got, want := os.Getenv("PATH"), "/usr/bin:/bin:/usr/local/bin:/Users/op/.docker/bin"; got != want {
		t.Errorf("PATH = %q, want %q", got, want)
	}
}

func TestEnsureToolPathIsIdempotent(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	exists := existing("/usr/local/bin")

	first, err := ensureToolPath("darwin", "/Users/op", exists)
	if err != nil || !slices.Contains(first, "/usr/local/bin") {
		t.Fatalf("first call = %q, %v; want it to add /usr/local/bin", first, err)
	}
	afterFirst := os.Getenv("PATH")

	second, err := ensureToolPath("darwin", "/Users/op", exists)
	if err != nil || len(second) != 0 {
		t.Errorf("second call = %q, %v; want nothing added", second, err)
	}
	if got := os.Getenv("PATH"); got != afterFirst {
		t.Errorf("PATH changed on the second call: %q, want %q", got, afterFirst)
	}
}

func TestEnsureToolPathWithoutHomeUsesSystemDirectories(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")

	added, err := ensureToolPath("darwin", "", func(string) bool { return true })
	if err != nil {
		t.Fatalf("ensureToolPath() error = %v", err)
	}
	for _, d := range added {
		if !filepath.IsAbs(d) || strings.Contains(d, ".docker") {
			t.Errorf("added %q with no home directory", d)
		}
	}
	if !slices.Contains(added, "/usr/local/bin") {
		t.Errorf("added = %q, want /usr/local/bin", added)
	}
}

func TestEnsureToolPathLeavesOtherSystemsAlone(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")

	added, err := ensureToolPath("linux", "/home/op", func(string) bool { return true })
	if err != nil || len(added) != 0 {
		t.Errorf("ensureToolPath(linux) = %q, %v; want nothing", added, err)
	}
	if got := os.Getenv("PATH"); got != "/usr/bin:/bin" {
		t.Errorf("PATH = %q on linux, want it unchanged", got)
	}
}

func TestIsDir(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "docker")
	if err := os.WriteFile(file, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{
		dir:                           true,
		file:                          false,
		filepath.Join(dir, "missing"): false,
	} {
		if got := isDir(path); got != want {
			t.Errorf("isDir(%q) = %v, want %v", path, got, want)
		}
	}
}
