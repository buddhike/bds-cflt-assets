package testenv

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// SHA is the commit the fake repository is at.
const SHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// Terraform source that follows the Launchpad interface, and one that also takes Confluent Cloud credentials.
const (
	GoodTF = `
variable "aws_region" { type = string }
variable "launchpad_tags" { type = map(string) }
output "launchpad_url" { value = "http://example" }
`
	CloudTF = GoodTF + `
variable "confluent_cloud_api_key" {
  type = string
}
variable "confluent_cloud_api_secret" {
  type      = string
  sensitive = true
}
`
)

// GitHub is a fake GitHub, and any other host, as an http.RoundTripper: a table of URL to response body.
// A URL that is not in the table is a 404.
type GitHub struct {
	mu     sync.Mutex
	routes map[string][]byte
	calls  []string
	// Headers is the headers of the latest request to each URL.
	Headers map[string]http.Header
}

// NewGitHub returns a GitHub with nothing in it.
func NewGitHub() *GitHub {
	return &GitHub{routes: map[string][]byte{}, Headers: map[string]http.Header{}}
}

// Route serves body at url.
func (g *GitHub) Route(url string, body []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.routes[url] = body
}

// Clear removes every route, as if GitHub were gone.
func (g *GitHub) Clear() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.routes = map[string][]byte{}
}

// Calls is every URL requested so far.
func (g *GitHub) Calls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.calls...)
}

// AddRepo serves a repository of files, a map of path to content, at SHA.
func (g *GitHub) AddRepo(files map[string]string) {
	g.AddRepoAt("owner/repo", SHA, files)
}

// AddRepoAt serves a repository at a commit.
func (g *GitHub) AddRepoAt(repo, sha string, files map[string]string) {
	g.Route("https://api.github.com/repos/"+repo+"/commits/main", []byte(sha))
	type entry struct {
		Path string `json:"path"`
		Type string `json:"type"`
	}
	var entries []entry
	dirs := map[string]bool{}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		parts := strings.Split(p, "/")
		for i := 1; i < len(parts); i++ {
			dirs[strings.Join(parts[:i], "/")] = true
		}
		entries = append(entries, entry{p, "blob"})
		g.Route("https://raw.githubusercontent.com/"+repo+"/"+sha+"/"+p, []byte(files[p]))
	}
	for d := range dirs {
		entries = append(entries, entry{d, "tree"})
	}
	tree, _ := json.Marshal(map[string]any{"tree": entries, "truncated": false})
	g.Route("https://api.github.com/repos/"+repo+"/git/trees/"+sha+"?recursive=1", tree)
}

// Member is one entry of a tarball.
type Member struct {
	Name string
	Data string
	Kind string // "file" (the default), "dir" or "symlink", whose Data is the target
}

// Tarball serves the repository as a gzip tarball.
func (g *GitHub) Tarball(members []Member) {
	g.Route("https://codeload.github.com/owner/repo/tar.gz/"+SHA, TarGz(members))
}

// TarGz builds a gzip tarball.
func TarGz(members []Member) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, m := range members {
		h := &tar.Header{Name: m.Name, Mode: 0o644}
		switch m.Kind {
		case "dir":
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
		case "symlink":
			h.Typeflag, h.Linkname = tar.TypeSymlink, m.Data
		case "exec":
			h.Typeflag, h.Mode, h.Size = tar.TypeReg, 0o755, int64(len(m.Data))
		default:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(m.Data))
		}
		if err := tw.WriteHeader(h); err != nil {
			panic(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(m.Data)); err != nil {
				panic(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		panic(err)
	}
	if err := gz.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// RoundTrip serves the table.
func (g *GitHub) RoundTrip(req *http.Request) (*http.Response, error) {
	url := req.URL.String()
	g.mu.Lock()
	g.calls = append(g.calls, url)
	g.Headers[url] = req.Header.Clone()
	body, ok := g.routes[url]
	g.mu.Unlock()
	status := http.StatusOK
	if !ok {
		status, body = http.StatusNotFound, []byte("not found")
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}, nil
}
