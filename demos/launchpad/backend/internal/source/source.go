// Package source downloads a demo's Terraform from the repository.
package source

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"launchpad/internal/apperr"
	"launchpad/internal/github"
	"launchpad/internal/services"
)

// Download extracts <demoPath>/ of the repository at sha into dest and returns dest.
//
// Only regular files and directories under demoPath are written, whatever
// else the archive holds, so a hostile archive cannot write elsewhere.
func Download(ctx context.Context, svc *services.Services, sha, demoPath, dest string) (string, error) {
	if err := os.RemoveAll(dest); err != nil {
		return "", fmt.Errorf("clear %s: %w", dest, err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dest, err)
	}
	rc, err := github.Tarball(ctx, svc, sha)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	gz, err := gzip.NewReader(rc)
	if err != nil {
		return "", fmt.Errorf("read the repository archive: %w", err)
	}
	defer gz.Close()
	if err := extract(tar.NewReader(gz), demoPath, dest); err != nil {
		return "", err
	}
	if info, err := os.Stat(filepath.Join(dest, "tf")); err != nil || !info.IsDir() {
		return "", apperr.New(502, "demo_missing", demoPath+"/tf is not in the repository at this version")
	}
	return dest, nil
}

func extract(tr *tar.Reader, demoPath, dest string) error {
	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	prefix := strings.Trim(demoPath, "/") + "/"
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read the repository archive: %w", err)
		}
		// GitHub archives hold one top directory named <repo>-<sha>.
		_, rel, _ := strings.Cut(header.Name, "/")
		if !strings.HasPrefix(rel, prefix) {
			continue
		}
		rel = rel[len(prefix):]
		isDir, isFile := header.Typeflag == tar.TypeDir, header.Typeflag == tar.TypeReg
		if rel == "" || (!isDir && !isFile) || strings.HasPrefix(rel, "/") {
			continue
		}
		target := filepath.Join(root, rel) // Join cleans away any ..
		if target != root && !strings.HasPrefix(target, root+string(filepath.Separator)) {
			continue
		}
		if isDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := writeFile(target, tr, header.Mode&0o111 != 0); err != nil {
			return err
		}
	}
}

func writeFile(target string, r io.Reader, executable bool) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if executable {
		mode = 0o755
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(target, mode) // the umask may have narrowed it
}

// ReadTF returns the top-level .tf files of a downloaded demo, by name, for the interface check.
func ReadTF(demoDir string) (map[string]string, error) {
	paths, err := filepath.Glob(filepath.Join(demoDir, "tf", "*.tf"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	files := make(map[string]string, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		files[filepath.Base(p)] = strings.ToValidUTF8(string(data), "�")
	}
	return files, nil
}
