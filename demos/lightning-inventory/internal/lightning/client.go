package lightning

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"lightninginv/internal/protocol"
)

// Errors a query can end in, besides a transport error.
var (
	// ErrAsync is a 202: the server decided the result is large or slow. The demo does not
	// poll for it, and counts it as an error.
	ErrAsync = errors.New("the result is asynchronous (202)")
	// ErrRateLimited is a 429.
	ErrRateLimited = errors.New("rate limited (429)")
	// ErrNotFound is a 404: for a query, the table is not available yet; for a status
	// call, there is none.
	ErrNotFound = errors.New("not found (404)")
)

// StatusError is any other non-success answer.
type StatusError struct{ Status int }

func (e *StatusError) Error() string { return fmt.Sprintf("unexpected HTTP status %d", e.Status) }

// Client reports whether the status is a 4xx: the request is refused, and repeating it soon
// will not change that (credentials, permissions), unlike a 5xx.
func (e *StatusError) Client() bool { return e.Status >= 400 && e.Status < 500 }

const maxResponse = 16 << 20

// Auth is an API key and secret for HTTP Basic.
type Auth struct{ Key, Secret string }

func (a Auth) apply(r *http.Request) { r.SetBasicAuth(a.Key, a.Secret) }

// Querier runs SQL against one cluster's Lightning Tables.
type Querier struct {
	// Endpoint is https://sql.<region>.aws.confluent.cloud/query/v1.
	Endpoint string
	Catalog  string // environment id
	Database string // Kafka cluster id
	Auth     Auth
	HTTP     *http.Client
}

// NewQuerier returns a Querier for the documented endpoint of a region.
func NewQuerier(region, environment, cluster string, auth Auth, hc *http.Client) *Querier {
	return &Querier{
		Endpoint: fmt.Sprintf("https://sql.%s.aws.confluent.cloud/query/v1", region),
		Catalog:  environment, Database: cluster, Auth: auth, HTTP: hc,
	}
}

type queryRequest struct {
	CatalogName  string       `json:"catalog_name"`
	DatabaseName string       `json:"database_name"`
	Query        string       `json:"query"`
	Options      queryOptions `json:"options"`
}

type queryOptions struct {
	MaxResultRows int    `json:"max_result_rows"`
	ResultFormat  string `json:"result_format"`
}

// MaxResultRows caps a response: by_locations returns at most 2000 rows.
const MaxResultRows = 2500

// Query runs sql and returns how many rows came back. It reads the whole body, so the
// caller's timer covers the transfer too.
func (q *Querier) Query(ctx context.Context, sql string) (rows int, err error) {
	body, _ := json.Marshal(queryRequest{
		CatalogName: q.Catalog, DatabaseName: q.Database, Query: sql,
		Options: queryOptions{MaxResultRows: MaxResultRows, ResultFormat: "JSON"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, q.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", protocol.UserAgent)
	q.Auth.apply(req)
	resp, err := q.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return 0, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusAccepted:
		return 0, ErrAsync
	case http.StatusTooManyRequests:
		return 0, ErrRateLimited
	case http.StatusNotFound:
		return 0, ErrNotFound
	default:
		return 0, &StatusError{Status: resp.StatusCode}
	}
	var out struct {
		Result struct {
			Data []json.RawMessage `json:"data"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, fmt.Errorf("the response is not the documented JSON: %w", err)
	}
	return len(out.Result.Data), nil
}

// Phase is a Lightning Table's lifecycle phase, as the rtce-topics API reports it.
type Phase string

// The documented phases.
const (
	PhasePending      Phase = "PENDING"
	PhaseProvisioning Phase = "PROVISIONING"
	PhaseActive       Phase = "ACTIVE"
	PhaseDeleting     Phase = "DELETING"
	PhaseFailed       Phase = "FAILED"
	PhaseUnavailable  Phase = "UNAVAILABLE"
)

// Tables reads the Lightning Table of one topic. It only reads: Terraform
// (confluent_rtce_topic) enables and disables the table.
type Tables struct {
	// BaseURL is https://api.confluent.cloud.
	BaseURL     string
	Environment string
	Cluster     string
	Topic       string
	Auth        Auth
	HTTP        *http.Client
}

// NewTables returns Tables for the real API.
func NewTables(environment, cluster, topic string, auth Auth, hc *http.Client) *Tables {
	return &Tables{BaseURL: "https://api.confluent.cloud", Environment: environment, Cluster: cluster, Topic: topic, Auth: auth, HTTP: hc}
}

func (t *Tables) scope() string {
	v := url.Values{}
	v.Set("environment", t.Environment)
	v.Set("spec.kafka_cluster", t.Cluster)
	return v.Encode()
}

// Status reads the table's phase. ErrNotFound means there is no table (yet).
func (t *Tables) Status(ctx context.Context) (Phase, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.BaseURL+"/rtce/v1/rtce-topics/"+url.PathEscape(t.Topic)+"?"+t.scope(), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", protocol.UserAgent)
	t.Auth.apply(req)
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", "", ErrNotFound
	case http.StatusTooManyRequests:
		return "", "", ErrRateLimited
	default:
		return "", "", &StatusError{Status: resp.StatusCode}
	}
	var out struct {
		Status struct {
			Phase        Phase  `json:"phase"`
			ErrorMessage string `json:"error_message"`
		} `json:"status"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "", fmt.Errorf("the response is not the documented JSON: %w", err)
	}
	return out.Status.Phase, out.Status.ErrorMessage, nil
}
