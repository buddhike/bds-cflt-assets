package lightning

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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

// StatusError is any other non-success answer. Detail is what Confluent said was wrong
// (the code and detail of its first error), when the answer has it: a 403 names the missing
// role there.
type StatusError struct {
	Status int
	Detail string
}

func (e *StatusError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("unexpected HTTP status %d", e.Status)
	}
	return fmt.Sprintf("unexpected HTTP status %d: %s", e.Status, e.Detail)
}

const maxDetail = 300

// errorDetail reads the first error of a Confluent error body, {"errors":[{"code","title",
// "detail"}]}, and returns "" for anything else. It is short, so the UI and the log stay so.
func errorDetail(raw []byte) string {
	var b struct {
		Errors []struct {
			Code, Title, Detail string
		} `json:"errors"`
	}
	if json.Unmarshal(raw, &b) != nil || len(b.Errors) == 0 {
		return ""
	}
	e := b.Errors[0]
	msg := e.Detail
	if msg == "" {
		msg = e.Title
	}
	var parts []string
	for _, p := range []string{e.Code, msg} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	// One line, whatever the body held, and short.
	d := strings.Join(strings.Fields(strings.Join(parts, ": ")), " ")
	if r := []rune(d); len(r) > maxDetail {
		d = string(r[:maxDetail]) + "…"
	}
	return d
}

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

// maxPages bounds how many pages of the list Status follows.
const maxPages = 10

// Status reads the table's phase from the list of the cluster's tables (the by-name read is
// refused to the status account's key). ErrNotFound means the topic has no table (yet).
func (t *Tables) Status(ctx context.Context) (Phase, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	base, err := url.Parse(t.BaseURL)
	if err != nil {
		return "", "", err
	}
	next := "" // the first page
	for range maxPages {
		page, err := t.listPage(ctx, next)
		if err != nil {
			return "", "", err
		}
		for _, d := range page.Data {
			if d.Spec.TopicName == t.Topic {
				return d.Status.Phase, d.Status.ErrorMessage, nil
			}
		}
		if page.Metadata.Next == "" {
			return "", "", ErrNotFound
		}
		// The next page is a URL Confluent gave. It is followed only on the same host, since
		// the request carries the key.
		u, err := url.Parse(page.Metadata.Next)
		if err != nil || u.Scheme != base.Scheme || u.Host != base.Host {
			return "", "", errors.New("the list's next page is not on the API host")
		}
		next = page.Metadata.Next
	}
	return "", "", fmt.Errorf("the table is not in the first %d pages of the list", maxPages)
}

type tablesPage struct {
	Data []struct {
		Spec struct {
			TopicName string `json:"topic_name"`
		} `json:"spec"`
		Status struct {
			Phase        Phase  `json:"phase"`
			ErrorMessage string `json:"error_message"`
		} `json:"status"`
	} `json:"data"`
	Metadata struct {
		Next string `json:"next"`
	} `json:"metadata"`
}

// listPage reads one page of the list: the first when target is empty, else a next-page URL.
func (t *Tables) listPage(ctx context.Context, target string) (tablesPage, error) {
	var page tablesPage
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cmp.Or(target, t.BaseURL+"/rtce/v1/rtce-topics?"+t.scope()), nil)
	if err != nil {
		return page, err
	}
	req.Header.Set("User-Agent", protocol.UserAgent)
	t.Auth.apply(req)
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return page, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return page, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return page, ErrNotFound
	case http.StatusTooManyRequests:
		return page, ErrRateLimited
	default:
		return page, &StatusError{Status: resp.StatusCode, Detail: errorDetail(raw)}
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return page, fmt.Errorf("the response is not the documented JSON: %w", err)
	}
	return page, nil
}
