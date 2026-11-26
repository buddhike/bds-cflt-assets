// Package catalog is the list of demos, discovered from the repository and cached in DynamoDB.
package catalog

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"launchpad/internal/db"
	"launchpad/internal/demoif"
	"launchpad/internal/github"
	"launchpad/internal/services"
)

const CacheSeconds = 600

// MainBranch is the branch whose tip a demo's commit is compared with.
const MainBranch = "main"

const (
	maxName        = 80
	maxDescription = 500
	partition      = "CATALOG"
	demoPrefix     = "DEMO#"
)

// A demo id ends up in tags, state keys and AWS names, so keep it plain.
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// Demo is one entry of the catalog.
type Demo struct {
	ID          string `dynamodbav:"id" json:"id"`
	Name        string `dynamodbav:"name" json:"name"`
	Description string `dynamodbav:"description" json:"description"`
	Path        string `dynamodbav:"path" json:"path"`
	Ref         string `dynamodbav:"ref" json:"ref"`
	// Latest is whether Ref was the tip of MainBranch when the catalog was read.
	Latest bool `dynamodbav:"latest" json:"latest"`
	// Version is Git's id of the demo's directory at Ref. Unlike Ref, which is the
	// whole repository's commit, it changes only when something in the demo changes.
	Version        string   `dynamodbav:"version" json:"version"`
	Valid          bool     `dynamodbav:"valid" json:"valid"`
	Problems       []string `dynamodbav:"problems" json:"problems"`
	NeedsConfluent bool     `dynamodbav:"needsConfluent" json:"needsConfluent"`
}

type demoItem struct {
	PK string `dynamodbav:"pk"`
	SK string `dynamodbav:"sk"`
	Demo
}

type meta struct {
	PK          string `dynamodbav:"pk"`
	SK          string `dynamodbav:"sk"`
	RefreshedAt int64  `dynamodbav:"refreshedAt"`
	Ref         string `dynamodbav:"ref"`
}

// ValidID reports whether s can be a demo id.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// Refresh re-reads the repository and replaces the cached catalog.
func Refresh(ctx context.Context, svc *services.Services) ([]Demo, error) {
	sha, err := github.ResolveRef(ctx, svc)
	if err != nil {
		return nil, err
	}
	latest := sha
	if svc.Settings.DemosRef != MainBranch {
		if latest, err = github.Resolve(ctx, svc, MainBranch); err != nil {
			// Not worth failing the refresh: the demos are still listed, without the badge.
			svc.Log.Warn("Could not resolve the main branch", "error", err)
			latest = ""
		}
	}
	entries, err := github.Tree(ctx, svc, sha)
	if err != nil {
		return nil, err
	}
	base := svc.Settings.DemosPath + "/"
	kinds := make(map[string]string, len(entries))
	shas := make(map[string]string, len(entries))
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		if _, seen := kinds[e.Path]; !seen {
			paths = append(paths, e.Path)
		}
		kinds[e.Path] = e.Type
		shas[e.Path] = e.SHA
	}
	sort.Strings(paths)

	demos := []Demo{}
	for _, path := range paths {
		if kinds[path] != "tree" || !strings.HasPrefix(path, base) {
			continue
		}
		id := path[len(base):]
		if strings.Contains(id, "/") || kinds[path+"/tf"] != "tree" {
			continue
		}
		if !idPattern.MatchString(id) {
			svc.Log.Warn("Skipping a demo whose directory name is not a valid id", "id", id)
			continue
		}
		demo, err := describe(ctx, svc, sha, id, path, paths, kinds)
		if err != nil {
			return nil, err
		}
		demo.Latest = sha == latest
		demo.Version = shas[path]
		demos = append(demos, demo)
	}

	wanted := map[string]bool{}
	for _, d := range demos {
		wanted[demoPrefix+d.ID] = true
	}
	old, err := svc.DB.Query(ctx, partition, demoPrefix)
	if err != nil {
		return nil, err
	}
	for _, item := range old {
		var it demoItem
		if err := db.Unmarshal(item, &it); err != nil {
			return nil, err
		}
		if !wanted[it.SK] {
			if err := svc.DB.Delete(ctx, partition, it.SK, nil); err != nil {
				return nil, err
			}
		}
	}
	for _, d := range demos {
		if err := db.PutAs(ctx, svc.DB, demoItem{PK: partition, SK: demoPrefix + d.ID, Demo: d}, nil); err != nil {
			return nil, err
		}
	}
	if err := db.PutAs(ctx, svc.DB, meta{PK: partition, SK: "META", RefreshedAt: svc.Now(), Ref: sha}, nil); err != nil {
		return nil, err
	}
	return demos, nil
}

func describe(ctx context.Context, svc *services.Services, sha, id, path string, paths []string, kinds map[string]string) (Demo, error) {
	var info struct {
		Name        any `json:"name"`
		Description any `json:"description"`
	}
	if kinds[path+"/demo.json"] == "blob" {
		raw, err := github.Raw(ctx, svc, sha, path+"/demo.json")
		if err != nil {
			return Demo{}, err
		}
		// A demo.json that is not an object is ignored, like one that is not JSON.
		if err := json.Unmarshal([]byte(raw), &info); err != nil {
			svc.Log.Warn("Ignoring demo.json that is not valid JSON", "path", path)
			info.Name, info.Description = nil, nil
		}
	}

	tfFiles := map[string]string{}
	tfDir := path + "/tf/"
	for _, p := range paths {
		if kinds[p] != "blob" || !strings.HasPrefix(p, tfDir) || !strings.HasSuffix(p, ".tf") {
			continue
		}
		name := p[len(tfDir):]
		if strings.Contains(name, "/") {
			continue
		}
		text, err := github.Raw(ctx, svc, sha, p)
		if err != nil {
			return Demo{}, err
		}
		tfFiles[name] = text
	}
	iface := demoif.Check(tfFiles)

	name := text(info.Name)
	if name == "" {
		name = id
	}
	return Demo{
		ID:             id,
		Name:           truncate(name, maxName),
		Description:    truncate(text(info.Description), maxDescription),
		Path:           path,
		Ref:            sha,
		Valid:          iface.Valid(),
		Problems:       iface.Problems,
		NeedsConfluent: iface.NeedsConfluent,
	}, nil
}

// text is a demo.json value as text. Only strings count: a name or
// description of any other type is ignored.
func text(v any) string {
	s, _ := v.(string)
	return s
}

func truncate(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}

// List is the cached catalog, refreshed when it is older than CacheSeconds.
func List(ctx context.Context, svc *services.Services, force bool) ([]Demo, error) {
	current, err := db.GetAs[meta](ctx, svc.DB, partition, "META")
	if err != nil {
		return nil, err
	}
	stale := current == nil || svc.Now()-current.RefreshedAt >= CacheSeconds
	if force || stale {
		demos, err := Refresh(ctx, svc)
		if err == nil {
			return demos, nil
		}
		if force || current == nil {
			return nil, err
		}
		svc.Log.Error("Could not refresh the demo catalog, using the cached one", "error", err)
	}
	items, err := db.QueryAs[demoItem](ctx, svc.DB, partition, demoPrefix)
	if err != nil {
		return nil, err
	}
	demos := make([]Demo, len(items))
	for i, it := range items {
		demos[i] = it.Demo
	}
	return demos, nil
}

// Get is one demo, or nil if there is no such demo.
func Get(ctx context.Context, svc *services.Services, id string) (*Demo, error) {
	if !idPattern.MatchString(id) {
		return nil, nil
	}
	current, err := db.GetAs[meta](ctx, svc.DB, partition, "META")
	if err != nil {
		return nil, err
	}
	if current == nil {
		if _, err := List(ctx, svc, false); err != nil { // nobody has listed the demos yet
			return nil, err
		}
	}
	item, err := db.GetAs[demoItem](ctx, svc.DB, partition, demoPrefix+id)
	if err != nil || item == nil {
		return nil, err
	}
	return &item.Demo, nil
}
