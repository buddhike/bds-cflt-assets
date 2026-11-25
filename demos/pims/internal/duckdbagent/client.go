package duckdbagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"pims/internal/compose"
)

// Client runs commands through an agent.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// NewClient returns a client for the agent at baseURL, which must be a loopback
// address: the agent has no TLS, and its token must not cross a network.
func NewClient(baseURL, token string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return nil, fmt.Errorf("agent url must be http://host:port, got %q", baseURL)
	}
	if h := u.Hostname(); h != "127.0.0.1" && h != "localhost" && h != "::1" {
		return nil, fmt.Errorf("agent url must be a loopback address, got %q", h)
	}
	if token == "" {
		return nil, errors.New("agent token is required")
	}
	// No overall timeout: the caller's context bounds a run, which can take minutes.
	return &Client{base: strings.TrimRight(baseURL, "/"), token: token, http: &http.Client{}}, nil
}

// ExecLimit has the signature of compose.Project.ExecLimit, so query.Runner can use a Client.
// service is ignored: an agent serves one container.
func (c *Client) ExecLimit(ctx context.Context, service string, stdin io.Reader, redact []string, maxStdout int, cmd ...string) ([]byte, error) {
	in, err := io.ReadAll(stdin)
	if err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	body, err := json.Marshal(Request{Cmd: cmd, Stdin: string(in), MaxStdout: maxStdout})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+Path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set(TokenHeader, c.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call the DuckDB agent: %w", scrub(err.Error(), redact))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return nil, fmt.Errorf("the DuckDB agent answered %d: %s", res.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out Response
	if err := json.NewDecoder(io.LimitReader(res.Body, maxStdoutLimit*2)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode the DuckDB agent's answer: %w", err)
	}
	if out.TooLarge {
		return nil, fmt.Errorf("exec in %s: %w", service, compose.ErrOutputTooLarge)
	}
	if out.ExitCode != 0 {
		return out.Stdout, &compose.ExecError{
			Service: service,
			Err:     fmt.Errorf("exit status %d", out.ExitCode),
			Stderr:  scrub(out.Stderr, redact).Error(),
		}
	}
	return out.Stdout, nil
}

// Ready reports whether the agent is up.
func (c *Client) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/healthz", nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("the DuckDB agent answered %d", res.StatusCode)
	}
	return nil
}

// scrub removes secrets from text and returns it as an error value, for use in a message.
func scrub(s string, redact []string) error {
	for _, r := range redact {
		if r != "" {
			s = strings.ReplaceAll(s, r, "***")
		}
	}
	return errors.New(s)
}
