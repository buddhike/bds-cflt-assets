// Package tableflow turns Tableflow on and off for Kafka topics through the Confluent
// Cloud API. Launchpad mode uses it: Terraform owns the topics, and the server enables
// Tableflow on them once Debezium has registered their schemas, then disables it again
// before Terraform destroys the topics.
package tableflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"pims/internal/retry"
)

// DefaultEndpoint is the Confluent Cloud API.
const DefaultEndpoint = "https://api.confluent.cloud"

const (
	topicsPath = "/tableflow/v1/tableflow-topics"
	userAgent  = "pims"
)

// HTTPError is a response the API refused with.
type HTTPError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	msg := fmt.Sprintf("%s %s answered %d", e.Method, e.Path, e.StatusCode)
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

// Client enables and disables Tableflow on the topics of one Kafka cluster. It signs in
// with a Tableflow API key, which the demo's service account owns.
type Client struct {
	base        string
	key, secret string
	envID       string
	clusterID   string
	http        *http.Client
	log         *slog.Logger
	policy      retry.Policy
	// perTopic bounds Disable's work on one topic, so one that keeps failing cannot use the
	// time the others need.
	perTopic time.Duration
}

// New returns a client for the topics of clusterID in envID. The key is only sent over
// HTTPS; plain HTTP is accepted for loopback so tests can use httptest.
func New(baseURL, key, secret, envID, clusterID string, log *slog.Logger) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("parse the Confluent Cloud API url %q", baseURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return nil, fmt.Errorf("the Confluent Cloud API url must use https, got %q", u.Scheme)
	}
	if key == "" || secret == "" || envID == "" || clusterID == "" {
		return nil, errors.New("tableflow client needs an API key, a secret, an environment and a cluster")
	}
	return &Client{
		base:      strings.TrimRight(baseURL, "/"),
		key:       key,
		secret:    secret,
		envID:     envID,
		clusterID: clusterID,
		http:      &http.Client{Timeout: 20 * time.Second},
		log:       log.With("component", "tableflow"),
		// A schema that was registered a moment ago can still be unknown to Tableflow,
		// so a refusal is tried again for a while before it is reported.
		policy:   retry.Policy{Attempts: 12, Initial: 2 * time.Second, Max: 15 * time.Second},
		perTopic: 12 * time.Second,
	}, nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// Enable turns Tableflow on, as Iceberg tables in Confluent managed storage, for every
// topic. A topic that already has it is left alone, so the call can be repeated.
func (c *Client) Enable(ctx context.Context, topics []string) error {
	for _, topic := range topics {
		err := c.withRetry(ctx, "enable", topic, func(ctx context.Context) error {
			err := c.do(ctx, http.MethodPost, topicsPath, enableBody(topic, c.envID, c.clusterID))
			var he *HTTPError
			if errors.As(err, &he) && he.StatusCode == http.StatusConflict {
				return nil // already enabled
			}
			return err
		})
		if err != nil {
			return fmt.Errorf("enable Tableflow on %s: %w", topic, err)
		}
		c.log.Info("Tableflow is enabled", "topic", topic)
	}
	return nil
}

// Disable turns Tableflow off for every topic and leaves the Kafka topics alone. A topic
// that does not have it is skipped, so the call can be repeated. It tries every topic, for
// a bounded time each, and reports all that failed.
//
// A 404 counts as "not enabled". If an Enable was cancelled while its request was in
// flight, Tableflow may not show that topic yet, so a Disable straight after could miss it.
// The caller stops everything that enables before it calls Disable, which leaves that window
// as small as Tableflow's own delay; a repeated Disable closes it.
func (c *Client) Disable(ctx context.Context, topics []string) error {
	var errs []error
	for _, topic := range topics {
		q := url.Values{"environment": {c.envID}, "spec.kafka_cluster": {c.clusterID}}
		path := topicsPath + "/" + url.PathEscape(topic) + "?" + q.Encode()
		tctx, cancel := context.WithTimeout(ctx, c.perTopic)
		err := c.withRetry(tctx, "disable", topic, func(ctx context.Context) error {
			err := c.do(ctx, http.MethodDelete, path, nil)
			var he *HTTPError
			if errors.As(err, &he) && he.StatusCode == http.StatusNotFound {
				return nil // not enabled
			}
			return err
		})
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("disable Tableflow on %s: %w", topic, err))
			continue
		}
		c.log.Info("Tableflow is disabled", "topic", topic)
	}
	return errors.Join(errs...)
}

func (c *Client) withRetry(ctx context.Context, verb, topic string, fn func(context.Context) error) error {
	p := c.policy
	p.OnRetry = func(attempt int, err error, wait time.Duration) {
		c.log.Info("Trying Tableflow again", "operation", verb, "topic", topic, "attempt", attempt, "error", err, "wait", wait)
	}
	return retry.Do(ctx, p, fn)
}

func enableBody(topic, envID, clusterID string) []byte {
	type ref struct {
		ID string `json:"id"`
	}
	b, _ := json.Marshal(map[string]any{"spec": map[string]any{
		"display_name":  topic,
		"suspended":     false,
		"table_formats": []string{"ICEBERG"},
		"storage":       map[string]string{"kind": "Managed"},
		"environment":   ref{envID},
		"kafka_cluster": ref{clusterID},
	}})
	return b
}

// do sends one request. A refusal that waiting could fix (throttling, a server error, a
// schema Tableflow does not know yet) is returned as is; any other is Permanent.
func (c *Client) do(ctx context.Context, method, path string, body []byte) error {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return retry.Permanent(fmt.Errorf("build request: %w", err))
	}
	req.SetBasicAuth(c.key, c.secret)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, strings.SplitN(path, "?", 2)[0], err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return nil
	}
	he := &HTTPError{Method: method, Path: strings.SplitN(path, "?", 2)[0], StatusCode: resp.StatusCode, Body: detail(msg)}
	if retryable(resp.StatusCode) {
		return he
	}
	return retry.Permanent(he)
}

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusUnprocessableEntity || status >= 500
}

// detail is the message of an API error, or the first line of the body if it is not one.
func detail(b []byte) string {
	var e struct {
		Errors []struct {
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	if json.Unmarshal(b, &e) == nil && len(e.Errors) > 0 && e.Errors[0].Detail != "" {
		return e.Errors[0].Detail
	}
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
