// Package github gives read-only access to the repository the demos live in.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"launchpad/internal/apperr"
	"launchpad/internal/services"
)

var (
	repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	refPattern  = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,200}$`)
	shaPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

const (
	maxTreeBytes = 200 * 1024 * 1024
	maxRawBytes  = 1024 * 1024
	apiTimeout   = 20 * time.Second
	// A tarball is the whole repository, and it is read while it is extracted.
	tarballTimeout = 5 * time.Minute
)

// Entry is one path of a repository tree. Type is "blob" or "tree". SHA is
// Git's id of what is there: it changes when, and only when, a file at or below
// the path changes.
type Entry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

// body cancels the request's context when it is closed.
type body struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b body) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// open sends a GET. The caller closes the returned body. A status that is not
// a success becomes an *apperr.Error.
func open(ctx context.Context, svc *services.Services, url string, headers map[string]string, timeout time.Duration) (io.ReadCloser, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "launchpad")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if svc.Settings.GitHubToken != "" && strings.HasPrefix(url, "https://api.github.com/") {
		req.Header.Set("Authorization", "Bearer "+svc.Settings.GitHubToken)
	}
	resp, err := svc.HTTP.Do(req)
	if err != nil {
		cancel()
		return nil, apperr.New(502, "github_unreachable", "GitHub could not be reached")
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return body{resp.Body, cancel}, nil
	}
	_ = resp.Body.Close()
	cancel()
	switch resp.StatusCode {
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, apperr.New(502, "github_rate_limited", "GitHub is limiting requests. Try again later")
	case http.StatusNotFound:
		return nil, apperr.New(502, "github_not_found", "The demos repository or path was not found on GitHub")
	}
	return nil, apperr.New(502, "github_error", fmt.Sprintf("GitHub answered %d", resp.StatusCode))
}

func repo(svc *services.Services) (string, error) {
	r := svc.Settings.DemosRepo
	if !repoPattern.MatchString(r) {
		return "", fmt.Errorf("invalid repository %q", r)
	}
	return r, nil
}

// ResolveRef is the commit SHA the configured branch or tag points at now.
func ResolveRef(ctx context.Context, svc *services.Services) (string, error) {
	return Resolve(ctx, svc, svc.Settings.DemosRef)
}

// Resolve is the commit SHA a branch or tag of the repository points at now.
func Resolve(ctx context.Context, svc *services.Services, ref string) (string, error) {
	if !refPattern.MatchString(ref) {
		return "", fmt.Errorf("invalid ref %q", ref)
	}
	r, err := repo(svc)
	if err != nil {
		return "", err
	}
	rc, err := open(ctx, svc, fmt.Sprintf("https://api.github.com/repos/%s/commits/%s", r, ref),
		map[string]string{"Accept": "application/vnd.github.sha"}, apiTimeout)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, 100))
	if err != nil {
		return "", apperr.New(502, "github_unreachable", "GitHub could not be reached")
	}
	sha := strings.TrimSpace(string(data))
	if !shaPattern.MatchString(sha) {
		return "", apperr.New(502, "github_error", "GitHub returned an unexpected commit id")
	}
	return sha, nil
}

// Tree lists every path of the repository at sha.
func Tree(ctx context.Context, svc *services.Services, sha string) ([]Entry, error) {
	r, err := repo(svc)
	if err != nil {
		return nil, err
	}
	rc, err := open(ctx, svc, fmt.Sprintf("https://api.github.com/repos/%s/git/trees/%s?recursive=1", r, sha), nil, apiTimeout)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var data struct {
		Tree      *[]Entry `json:"tree"`
		Truncated bool     `json:"truncated"`
	}
	if err := json.NewDecoder(io.LimitReader(rc, maxTreeBytes)).Decode(&data); err != nil {
		return nil, fmt.Errorf("read the repository tree: %w", err)
	}
	if data.Truncated {
		return nil, apperr.New(502, "github_truncated", "The repository is too large to list its demos")
	}
	if data.Tree == nil {
		return nil, errors.New("the repository tree has no entries field")
	}
	return *data.Tree, nil
}

// Raw is the content of one file at sha, as text.
func Raw(ctx context.Context, svc *services.Services, sha, path string) (string, error) {
	r, err := repo(svc)
	if err != nil {
		return "", err
	}
	rc, err := open(ctx, svc, fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", r, sha, path), nil, apiTimeout)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxRawBytes))
	if err != nil {
		return "", apperr.New(502, "github_unreachable", "GitHub could not be reached")
	}
	return strings.ToValidUTF8(string(data), "�"), nil
}

// Tarball opens the repository at sha as a gzip tarball. The caller closes it.
func Tarball(ctx context.Context, svc *services.Services, sha string) (io.ReadCloser, error) {
	r, err := repo(svc)
	if err != nil {
		return nil, err
	}
	return open(ctx, svc, fmt.Sprintf("https://codeload.github.com/%s/tar.gz/%s", r, sha), nil, tarballTimeout)
}
