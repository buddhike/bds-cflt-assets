package api

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/aws/aws-lambda-go/events"

	"launchpad/internal/apperr"
)

// FromEvent converts an API Gateway payload (format 2.0) into a Request.
func FromEvent(event events.APIGatewayV2HTTPRequest) (*Request, error) {
	path := event.RawPath
	if path == "" {
		path = "/"
	}
	if strings.HasPrefix(path, "/api/") {
		path = path[len("/api"):]
	}
	path = strings.TrimRight(path, "/")
	if path == "" {
		path = "/"
	}

	cookies := map[string]string{}
	for _, c := range event.Cookies {
		name, value, _ := strings.Cut(c, "=")
		cookies[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}
	headers := make(map[string]string, len(event.Headers))
	for k, v := range event.Headers {
		headers[strings.ToLower(k)] = v
	}

	body := []byte(event.Body)
	if event.IsBase64Encoded {
		var err error
		if body, err = base64.StdEncoding.DecodeString(event.Body); err != nil {
			return nil, apperr.BadRequest("invalid_body", "The request body is not valid")
		}
	}
	return &Request{
		Method:  strings.ToUpper(event.RequestContext.HTTP.Method),
		Path:    path,
		Headers: headers,
		Cookies: cookies,
		Body:    body,
	}, nil
}

// ToEvent converts a Response into an API Gateway payload.
func ToEvent(resp Response) events.APIGatewayV2HTTPResponse {
	var body []byte
	if resp.Body == nil {
		body = []byte("{}")
	} else if encoded, err := json.Marshal(resp.Body); err == nil {
		body = encoded
	} else {
		resp.Status = 500
		body = []byte(`{"error":{"code":"internal","message":"Something went wrong"}}`)
	}
	return events.APIGatewayV2HTTPResponse{
		StatusCode: resp.Status,
		Headers:    map[string]string{"content-type": "application/json", "cache-control": "no-store"},
		Body:       string(body),
		Cookies:    resp.Cookies,
	}
}
