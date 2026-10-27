package vaif

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// OperationOptions supplies arguments to a spec-derived operation. JSON bodies
// accept structs or maps; binary bodies require []byte, text bodies string.
type OperationOptions struct {
	Path    map[string]string
	Query   url.Values
	Body    any
	Headers http.Header
}

// OperationParameter is a path or query parameter of a generated operation.
type OperationParameter struct {
	Name     string
	Required bool
}

// Operation describes one generated operation. Transport is "json", "binary"
// or "sse"; BodyKind is "json", "binary", "text" or empty for no body.
type Operation struct {
	Name, Method, Path, Transport, BodyKind string
	BodyRequired                            bool
	PathParams, QueryParams                 []OperationParameter
}

// UngeneratedOperation is a spec operation the generator skipped, with why.
type UngeneratedOperation struct{ Method, Path, Reason string }

// OperationsClient calls the generated OpenAPI operations. It shares the
// client's transport, session, retries and errors.
type OperationsClient struct{ client *Client }

func indexOperations(list []Operation) map[string]Operation {
	out := make(map[string]Operation, len(list))
	for _, op := range list {
		out[op.Name] = op
	}
	return out
}

// OperationsTable returns a copy of the generated operation table, keyed by
// method name. Modifying it does not change client routing.
func OperationsTable() map[string]Operation {
	out := make(map[string]Operation, len(generatedOperationList))
	for _, op := range generatedOperationList {
		op.PathParams = append([]OperationParameter(nil), op.PathParams...)
		op.QueryParams = append([]OperationParameter(nil), op.QueryParams...)
		out[op.Name] = op
	}
	return out
}

// UngeneratedOperations lists the spec operations without a generated method.
func UngeneratedOperations() []UngeneratedOperation {
	return append([]UngeneratedOperation(nil), ungeneratedOperations...)
}

func (o *OperationsClient) prepare(op Operation, args OperationOptions) (request, error) {
	// VAIF Studio accounts are created and reset in the console only:
	// refuse before any request.
	if op.Method == http.MethodPost && op.Path == "/auth/signup" {
		return request{}, consoleOnlySignUp(op.Name+"()", "call PostProjectsByProjectIdAuthSignup")
	}
	if op.Method == http.MethodPost && op.Path == "/auth/forgot-password" {
		return request{}, consoleOnlyPasswordReset(op.Name+"()", "call PostProjectsByProjectIdAuthForgotPassword")
	}
	path := op.Path
	for _, p := range op.PathParams {
		value := args.Path[p.Name]
		if value == "" {
			return request{}, invalidArg("missing path parameter %s", p.Name)
		}
		// Dot segments are not path IDs: proxies may normalize them after escaping.
		if value == "." || value == ".." {
			return request{}, invalidArg("invalid path parameter %s", p.Name)
		}
		path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(value))
	}
	for _, p := range op.QueryParams {
		if p.Required && len(args.Query[p.Name]) == 0 {
			return request{}, invalidArg("missing query parameter %s", p.Name)
		}
	}
	if op.BodyRequired && args.Body == nil {
		return request{}, invalidArg("request body is required")
	}
	r := request{method: op.Method, path: path, query: args.Query, header: args.Headers}
	switch op.BodyKind {
	case "json":
		r.body = args.Body
	case "binary":
		if args.Body != nil {
			data, ok := args.Body.([]byte)
			if !ok {
				return request{}, invalidArg("binary body requires []byte")
			}
			r.rawBody = data
			r.contentType = "application/octet-stream"
		}
	case "text":
		if args.Body != nil {
			s, ok := args.Body.(string)
			if !ok {
				return request{}, invalidArg("text body requires string")
			}
			r.rawBody = []byte(s)
			r.contentType = "text/plain"
		}
	default:
		if args.Body != nil {
			return request{}, invalidArg("operation does not accept a body")
		}
	}
	return r, nil
}
func (o *OperationsClient) call(ctx context.Context, op Operation, args OperationOptions) (*response, error) {
	r, e := o.prepare(op, args)
	if e != nil {
		return nil, e
	}
	return o.client.send(ctx, r, false)
}

func (o *OperationsClient) callBinary(ctx context.Context, op Operation, args OperationOptions) ([]byte, error) {
	r, err := o.call(ctx, op, args)
	if err != nil {
		return nil, err
	}
	return r.body, nil
}

// callJSON decodes a JSON operation's success body. A 204 (or any empty 2xx
// body) returns the zero value.
func callJSON[T any](ctx context.Context, o *OperationsClient, op Operation, args OperationOptions) (*T, error) {
	r, err := o.call(ctx, op, args)
	if err != nil {
		return nil, err
	}
	var out T
	if len(r.body) == 0 {
		return &out, nil
	}
	if err := decodeJSON(r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// stream returns the live SSE body. Callers must close it; the caller's context
// bounds its lifetime. A successful response is never buffered or retried,
// and a failed read of its body is an *Error (see eventStream).
func (o *OperationsClient) stream(ctx context.Context, op Operation, args OperationOptions) (*http.Response, error) {
	r, err := o.prepare(op, args)
	if err != nil {
		return nil, err
	}
	token, err := o.client.Auth.bearer(ctx)
	if err != nil {
		return nil, err
	}
	var payload []byte
	if r.body != nil {
		payload, err = json.Marshal(r.body)
		if err != nil {
			return nil, invalidArg("encoding body: %v", err)
		}
	} else {
		payload = r.rawBody
	}
	target := o.client.baseURL + r.path
	if len(r.query) > 0 {
		target += "?" + r.query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, r.method, target, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header = o.client.headers.Clone()
	for k, v := range r.header {
		req.Header[k] = append([]string(nil), v...)
	}
	if o.client.apiKey != "" {
		req.Header.Set(apiKeyHeader, o.client.apiKey)
	}
	req.Header.Set("Accept-Versions", "v1")
	req.Header.Set("User-Agent", "vaif-go/"+Version)
	req.Header.Set("Accept", "text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	} else if r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}
	resp, err := o.client.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, canceled(ctx.Err())
		}
		var sdkErr *Error
		if errors.As(err, &sdkErr) { // a refused redirect
			return nil, sdkErr
		}
		return nil, transportError(err, target, o.client.timeout)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, e := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		if e != nil {
			return nil, networkError(e, o.client.timeout)
		}
		return nil, errorFromResponse(resp.StatusCode, resp.Header, body, http.StatusText(resp.StatusCode))
	}
	resp.Body = &eventStream{ReadCloser: resp.Body, ctx: ctx}
	return resp, nil
}

// eventStream is a live event-stream body whose read failures are *Error
// values: the caller's canceled context as in do(), a timeout as ErrTimeout,
// any other failure as ErrNetwork, each wrapping the transport's error. The
// end of the stream is io.EOF, as io.Reader requires.
type eventStream struct {
	io.ReadCloser
	ctx context.Context
}

func (s *eventStream) Read(p []byte) (int, error) {
	n, err := s.ReadCloser.Read(p)
	if err == nil || err == io.EOF {
		return n, err
	}
	if s.ctx.Err() != nil {
		return n, canceled(s.ctx.Err())
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return n, &Error{Kind: ErrTimeout, Message: "event stream timed out", Err: err}
	}
	return n, &Error{Kind: ErrNetwork, Message: "event stream failed", Err: err}
}
