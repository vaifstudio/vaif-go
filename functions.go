package vaif

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// FunctionsClient invokes the project's deployed functions.
type FunctionsClient struct {
	client *Client
}

// InvokeOptions shapes the request the function sees.
type InvokeOptions struct {
	Method     string            // GET, POST (default), PUT, PATCH, DELETE
	Path       string            // default "/"
	Query      map[string]string // query parameters passed to the function
	Headers    map[string]string // headers passed to the function
	Timeout    time.Duration     // overrides the client timeout for this call
	Idempotent bool              // allow retries on transient failures
}

var invokeMethods = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}

// Invoke calls the function name (or id) with body (JSON-encoded; nil for
// none) and returns its HTTP response. A non-2xx response is returned as an
// *Error whose Body holds the function's response; a 401 the function
// returned is returned the same way and does not refresh the session.
// Invocations are not retried unless opts.Idempotent is set. When the API
// itself refuses the session's access token, the session is refreshed and
// the request sent once more: the function did not run.
func (f *FunctionsClient) Invoke(ctx context.Context, name string, body any, opts *InvokeOptions) (*FunctionResponse, error) {
	if name == "" {
		return nil, invalidArg("function name must not be empty")
	}
	o := InvokeOptions{}
	if opts != nil {
		o = *opts
	}
	method := strings.ToUpper(o.Method)
	if method == "" {
		method = "POST"
	}
	if !invokeMethods[method] {
		return nil, invalidArg("method must be GET, POST, PUT, PATCH or DELETE")
	}
	path := o.Path
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		return nil, invalidArg("path must start with '/'")
	}
	// Always the envelope form: a raw body that happened to contain "method"
	// or "path" keys would otherwise be misread by the API as an envelope.
	envelope := map[string]any{"method": method, "path": path}
	if len(o.Query) > 0 {
		envelope["query"] = o.Query
	}
	if len(o.Headers) > 0 {
		envelope["headers"] = o.Headers
	}
	if body != nil {
		envelope["body"] = body
	}
	resp, err := f.client.send(ctx, request{
		method:     http.MethodPost,
		path:       "/functions/" + url.PathEscape(name) + "/invoke",
		body:       envelope,
		timeout:    o.Timeout,
		idempotent: boolPtr(o.Idempotent),
	}, false)
	if err != nil {
		return nil, err
	}
	return &FunctionResponse{Status: resp.status, Header: resp.header, Body: resp.body}, nil
}
