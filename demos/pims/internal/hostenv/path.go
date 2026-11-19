// Package hostenv repairs the process environment when PIMS is started as a
// desktop app rather than from a terminal.
//
// macOS launches apps from Finder, the Dock and Spotlight with launchd's
// minimal PATH (/usr/bin:/bin:/usr/sbin:/sbin), not the one a shell profile
// builds. Docker Desktop, Homebrew and the other container runtimes install
// their CLIs elsewhere, so the docker CLI, and the credential helpers that
// docker and docker compose run through PATH, would not be found.
package hostenv

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// toolDirs lists where container runtimes put their CLIs on macOS, most
// common first: Docker Desktop's symlinks and bundle, Homebrew on Intel and
// Apple silicon (Colima, Lima), a per-user Docker Desktop, OrbStack and
// Rancher Desktop. The per-user entries are skipped unless home is absolute,
// so a stray relative $HOME cannot add a directory that depends on the
// working directory.
func toolDirs(home string) []string {
	dirs := []string{
		"/usr/local/bin",
		"/opt/homebrew/bin",
		"/Applications/Docker.app/Contents/Resources/bin",
	}
	if filepath.IsAbs(home) {
		dirs = append(dirs,
			filepath.Join(home, ".docker", "bin"),
			filepath.Join(home, "Applications", "Docker.app", "Contents", "Resources", "bin"),
			filepath.Join(home, ".orbstack", "bin"),
			filepath.Join(home, ".rd", "bin"),
		)
	}
	return dirs
}

// EnsureToolPath appends the usual container tool directories to PATH when
// they exist and are missing from it, and returns the ones it added.
//
// The directories go after the existing entries, never before: several of
// them are user-writable (Homebrew, ~/.docker/bin), and prepending them
// would let a binary there shadow a system tool such as bash or tar. A PATH
// that already works is never reordered. It does nothing outside macOS,
// where desktop launches inherit the login session's PATH.
func EnsureToolPath() ([]string, error) {
	home, _ := os.UserHomeDir()
	return ensureToolPath(runtime.GOOS, home, isDir)
}

// ensureToolPath is EnsureToolPath with the platform, home directory and
// directory check injected for tests.
func ensureToolPath(goos, home string, exists func(string) bool) ([]string, error) {
	if goos != "darwin" {
		return nil, nil
	}
	next, added := extendPath(os.Getenv("PATH"), toolDirs(home), exists)
	if len(added) == 0 {
		return nil, nil
	}
	if err := os.Setenv("PATH", next); err != nil {
		return nil, fmt.Errorf("set PATH: %w", err)
	}
	return added, nil
}

// extendPath appends each directory in dirs that satisfies exists and is not
// already an entry of path, and returns the new path and the directories it
// appended.
func extendPath(path string, dirs []string, exists func(string) bool) (string, []string) {
	seen := make(map[string]bool)
	for _, e := range filepath.SplitList(path) {
		seen[filepath.Clean(e)] = true
	}
	var added []string
	for _, d := range dirs {
		d = filepath.Clean(d)
		if seen[d] || !exists(d) {
			continue
		}
		seen[d] = true
		added = append(added, d)
	}
	if len(added) == 0 {
		return path, nil
	}
	parts := added
	if path != "" {
		parts = append([]string{path}, added...)
	}
	return strings.Join(parts, string(os.PathListSeparator)), added
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
