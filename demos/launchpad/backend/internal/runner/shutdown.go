package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"launchpad/internal/credentials"
	"launchpad/internal/terraform"
)

const (
	// shutdownOutput is the optional output through which a demo asks to be called before
	// it is destroyed. Its value is JSON: {"url": "https://...", "token": "..."}.
	shutdownOutput = "launchpad_shutdown"

	shutdownAttempts = 3
	// shutdownTimeout covers one request. A demo is told to answer within a minute.
	shutdownTimeout = 65 * time.Second
	// maxHookReply is how much of a demo's answer is read.
	maxHookReply = 4096
)

// hook is what the demo's launchpad_shutdown output says.
type hook struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

func parseHook(raw string) (hook, error) {
	var h hook
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		return hook{}, errors.New("it is not JSON with a url and a token")
	}
	u, err := url.Parse(h.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		// The token travels with the request, so nothing but HTTPS will do.
		return hook{}, errors.New("the url must be an https address")
	}
	if h.Token == "" {
		return hook{}, errors.New("the token is empty")
	}
	return h, nil
}

// shutDownDemo asks a demo that wants it to shut itself down, before Terraform destroys
// it. It does nothing for a demo with no launchpad_shutdown output, and for one that
// was never applied.
//
// Only an answer from the demo that says it tried and failed stops the teardown, and the
// person can retry it. A demo that does not answer at all (no connection, or a gateway that
// has no one behind it) cannot be asked and must still be destroyable, so the destroy goes
// on. A demo whose output is unusable is treated the same way: it cannot be fixed from here.
func (a *slice) shutDownDemo(ctx context.Context, tf *terraform.Terraform, creds *credentials.Credentials) error {
	raw, err := readOutput(ctx, tf, creds, shutdownOutput, false)
	if err != nil {
		return err
	}
	if raw == "" {
		return nil
	}
	log := a.tail
	h, err := parseHook(raw)
	if err != nil {
		log.Add("Launchpad: not asking the demo to shut down, because its " + shutdownOutput + " output is unusable: " + err.Error())
		a.Svc.Log.Warn("Unusable shutdown output", "demo", a.ev.DemoID, "problem", err.Error())
		return nil
	}
	log.Hide(h.Token)
	log.Add("Launchpad: asking the demo to shut down")

	client := a.HTTP
	if client == nil {
		client = &http.Client{
			Timeout:       shutdownTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	var last string
	for attempt := 1; attempt <= shutdownAttempts; attempt++ {
		status, reply, err := postShutdown(ctx, client, h)
		reply = strings.ReplaceAll(reply, h.Token, "***")
		switch {
		case err == nil && status >= 200 && status <= 299:
			log.Add("Launchpad: the demo is shut down")
			return nil
		case err == nil && !notThere(status):
			return failure(fmt.Sprintf("The demo could not shut down (HTTP %d): %s. Retry the teardown", status, reply))
		case err != nil:
			last = "no answer"
		default:
			last = fmt.Sprintf("HTTP %d", status)
		}
		log.Add(fmt.Sprintf("Launchpad: the demo did not answer (%s), attempt %d of %d", last, attempt, shutdownAttempts))
		if attempt < shutdownAttempts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(a.ShutdownBackoff):
			}
		}
	}
	log.Add("Launchpad: the demo is not answering, so it is destroyed as it is")
	return nil
}

// notThere is a status that a gateway in front of a demo gives when nothing answers behind it.
func notThere(status int) bool {
	return status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// postShutdown makes one request. The reply is the demo's message, for a person to read.
func postShutdown(ctx context.Context, client interface {
	Do(*http.Request) (*http.Response, error)
}, h hook) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+h.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "launchpad")
	res, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, maxHookReply))
	return res.StatusCode, replyText(body), nil
}

// replyText is the "error" of a JSON answer, or else its first line.
func replyText(body []byte) string {
	var j struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &j) == nil && j.Error != "" {
		return j.Error
	}
	text := strings.TrimSpace(string(body))
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	if len(text) > 300 {
		text = strings.ToValidUTF8(text[:300], "")
	}
	if text == "" {
		return "no message"
	}
	return text
}
