package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Registry is a minimal Confluent Cloud Schema Registry client used to
// confirm Debezium has registered the topic schemas. Tableflow cannot be
// enabled on a topic until its value schema exists.
type Registry struct {
	baseURL   string
	apiKey    string
	apiSecret string
	http      *http.Client
	log       *slog.Logger
	// pollInterval controls subject polling; overridden in tests.
	pollInterval time.Duration
}

// NewRegistry returns a client for the Schema Registry at baseURL. Basic
// auth credentials are only ever sent over HTTPS; plain HTTP is accepted
// for loopback addresses so tests can use httptest.
func NewRegistry(baseURL, apiKey, apiSecret string, log *slog.Logger) (*Registry, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse schema registry url: %w", err)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return nil, fmt.Errorf("schema registry url must use https, got %q", u.Scheme)
	}
	return &Registry{
		baseURL:      strings.TrimRight(baseURL, "/"),
		apiKey:       apiKey,
		apiSecret:    apiSecret,
		http:         &http.Client{Timeout: 15 * time.Second},
		log:          log.With("component", "schema-registry"),
		pollInterval: 5 * time.Second,
	}, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Subjects lists registered subjects.
func (r *Registry) Subjects(ctx context.Context) ([]string, error) {
	b, err := doJSON(ctx, r.http, http.MethodGet, r.baseURL, "/subjects", nil, func(req *http.Request) {
		req.SetBasicAuth(r.apiKey, r.apiSecret)
	})
	if err != nil {
		return nil, fmt.Errorf("list schema registry subjects: %w", err)
	}
	var subjects []string
	if err := json.Unmarshal(b, &subjects); err != nil {
		return nil, fmt.Errorf("decode subjects: %w", err)
	}
	return subjects, nil
}

// WaitForValueSchemas polls until "<topic>-value" exists for every topic.
// Transient errors are logged and polling continues until ctx is done.
func (r *Registry) WaitForValueSchemas(ctx context.Context, topics []string) error {
	t := time.NewTicker(r.pollInterval)
	defer t.Stop()
	for {
		subjects, err := r.Subjects(ctx)
		if err != nil {
			r.log.Warn("Could not list schema subjects", "error", err)
		} else {
			missing := missingValueSubjects(subjects, topics)
			if len(missing) == 0 {
				r.log.Info("Topic schemas are registered", "topics", topics)
				return nil
			}
			r.log.Info("Waiting for Debezium to register schemas", "missing", missing)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for topic schemas: %w", ctx.Err())
		case <-t.C:
		}
	}
}

func missingValueSubjects(subjects, topics []string) []string {
	var missing []string
	for _, topic := range topics {
		if !slices.Contains(subjects, topic+"-value") {
			missing = append(missing, topic+"-value")
		}
	}
	return missing
}
