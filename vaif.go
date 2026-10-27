package vaif

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Version is the SDK version, sent in the User-Agent header.
const Version = "1.0.0"

const (
	// DefaultBaseURL is the production API origin.
	DefaultBaseURL = "https://api.vaif.studio"
	// DefaultTimeout bounds each HTTP attempt.
	DefaultTimeout = 30 * time.Second

	apiKeyHeader = "x-vaif-key"
	// maxResponseBytes caps how much of a response body is read into memory.
	maxResponseBytes = 64 << 20
)

// RetryPolicy controls retries of transient failures.
//
// Only replay-safe requests (GET, HEAD, OPTIONS, or calls marked idempotent)
// are retried on RetryOn statuses and network errors. A request that failed
// before a connection was established is retried for every method, since the
// server never saw it.
type RetryPolicy struct {
	MaxRetries          int           // retries after the first attempt; 0 disables
	BaseDelay           time.Duration // delay before the first retry
	MaxDelay            time.Duration // cap on any single delay
	Multiplier          float64       // exponential growth factor
	RetryOn             []int         // statuses treated as transient
	RetryOnNetworkError bool
}

// DefaultRetryPolicy retries up to 3 times on 429, 500, 502, 503 and 504
// responses and on network errors, waiting 1s before the first retry and
// doubling the wait up to 30s.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries:          3,
		BaseDelay:           time.Second,
		MaxDelay:            30 * time.Second,
		Multiplier:          2,
		RetryOn:             []int{429, 500, 502, 503, 504},
		RetryOnNetworkError: true,
	}
}

// NoRetry disables retries.
func NoRetry() RetryPolicy { return RetryPolicy{} }

func (p RetryPolicy) retriesStatus(status int) bool {
	for _, s := range p.RetryOn {
		if s == status {
			return true
		}
	}
	return false
}

// delay returns the wait before retry number attempt (0-based), honoring a
// server Retry-After hint and adding up to 25% jitter.
func (p RetryPolicy) delay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if p.MaxDelay > 0 && retryAfter > p.MaxDelay {
			return p.MaxDelay
		}
		return retryAfter
	}
	mult := p.Multiplier
	if mult <= 0 {
		mult = 2
	}
	d := float64(p.BaseDelay)
	for i := 0; i < attempt; i++ {
		d *= mult
	}
	if p.MaxDelay > 0 && d > float64(p.MaxDelay) {
		d = float64(p.MaxDelay)
	}
	return time.Duration(d + d*rand.Float64()*0.25)
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL sets the API origin. It must use https; plain http is accepted
// only for localhost, 127.0.0.1 and ::1.
func WithBaseURL(baseURL string) Option { return func(c *Client) { c.baseURL = baseURL } }

// WithHTTPClient supplies the *http.Client used for every request.
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.http = hc } }

// WithTimeout bounds each HTTP attempt (default 30s). The caller's context
// deadline still applies on top.
func WithTimeout(d time.Duration) Option { return func(c *Client) { c.timeout = d } }

// WithRetryPolicy replaces the retry policy.
func WithRetryPolicy(p RetryPolicy) Option { return func(c *Client) { c.retry = p } }

// WithHeader adds a header to every API request.
func WithHeader(key, value string) Option {
	return func(c *Client) { c.headers.Set(key, value) }
}

// WithSession restores a previously persisted end-user session. NewClient
// keeps its own copy and applies SetSession's rules: AccessToken must not be
// empty, and a zero ExpiresAt is treated as one hour from now.
func WithSession(s *Session) Option { return func(c *Client) { c.initialSession = s } }

// Client talks to one VAIF Studio project. It is safe for concurrent use.
type Client struct {
	projectID      string
	apiKey         string
	baseURL        string
	http           *http.Client
	timeout        time.Duration
	retry          RetryPolicy
	headers        http.Header
	initialSession *Session
	sleep          func(ctx context.Context, d time.Duration) error

	// Auth signs end users in and keeps their session fresh.
	Auth *AuthClient
	// Storage uploads, downloads and signs URLs for objects.
	Storage *StorageClient
	// Functions invokes deployed functions.
	Functions *FunctionsClient
	// Operations is the generated OpenAPI layer.
	Operations *OperationsClient
	// Realtime subscribes to database changes.
	Realtime *RealtimeClient
}

// NewClient creates a client for projectID, authenticating with a project API
// key (sent as x-vaif-key). Use vk_pub_ keys in apps; vk_srv_ keys bypass
// row-level security and belong on trusted servers only.
func NewClient(projectID, apiKey string, opts ...Option) (*Client, error) {
	c := &Client{
		projectID: projectID,
		apiKey:    apiKey,
		baseURL:   DefaultBaseURL,
		timeout:   DefaultTimeout,
		retry:     DefaultRetryPolicy(),
		headers:   http.Header{},
		sleep:     sleepContext,
	}
	for _, opt := range opts {
		opt(c)
	}
	if strings.TrimSpace(projectID) == "" {
		return nil, invalidArg("projectID must not be empty")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, invalidArg("apiKey must not be empty")
	}
	if c.timeout <= 0 {
		return nil, invalidArg("timeout must be positive")
	}
	if c.retry.MaxRetries < 0 {
		return nil, invalidArg("MaxRetries must be >= 0")
	}
	base, err := normalizeBaseURL(c.baseURL)
	if err != nil {
		return nil, err
	}
	c.baseURL = base
	if c.http == nil {
		c.http = &http.Client{}
	}
	c.http = withRedirectPolicy(c.http)
	var session *Session
	if s := c.initialSession; s != nil {
		if s.AccessToken == "" {
			return nil, invalidArg("WithSession: AccessToken must not be empty")
		}
		copied := *s
		if copied.ExpiresAt.IsZero() {
			copied.ExpiresAt = time.Now().Add(time.Hour)
		}
		session = &copied
	}
	c.initialSession = nil
	c.Auth = &AuthClient{client: c, session: session}
	c.Storage = &StorageClient{client: c}
	c.Functions = &FunctionsClient{client: c}
	c.Operations = &OperationsClient{client: c}
	c.Realtime = newRealtimeClient(c)
	return c, nil
}

// ProjectID returns the project this client targets.
func (c *Client) ProjectID() string { return c.projectID }

// BaseURL returns the normalized API origin.
func (c *Client) BaseURL() string { return c.baseURL }

// From starts an immutable query against table.
func (c *Client) From(table string) *Query {
	q := &Query{client: c, table: table}
	if !isIdentifier(table) {
		q.err = invalidArg("table must be a SQL identifier (letters, digits, _), got %q", table)
	}
	return q
}

func normalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", invalidArg("base URL must be an absolute http(s) URL")
	}
	if u.Scheme == "http" && !allowsCleartext(u.Hostname()) {
		return "", invalidArg("base URL must use https (http is accepted only for localhost, 127.0.0.1 and ::1)")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// allowsCleartext reports whether host (as url.URL.Hostname returns it) may be
// reached without TLS: plain http, and plain ws for realtime, are for these
// loopback hosts only. normalizeBaseURL and realtimeURL both ask it.
func allowsCleartext(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// redirectKeeps are the headers a redirect to another origin keeps. Every
// other header of the first request, including the project key, the bearer
// token, cookies, WithHeader headers and headers passed for one call, is
// removed.
var redirectKeeps = map[string]bool{
	"Accept": true, "Accept-Encoding": true, "Accept-Language": true,
	"Content-Type": true, "Range": true, "User-Agent": true,
}

// withRedirectPolicy returns a copy of hc whose redirects never carry API
// credentials to another origin and never move to plain http; the caller's
// *http.Client is not changed. GET /storage/download answers with a 307 to a
// presigned object-store URL, and net/http copies every header except
// Authorization and Cookie to a redirect on another host, and follows a
// redirect from https to http. So a redirect is refused unless it is https
// (http only for the hosts allowsCleartext names), and once a redirect leaves
// the origin of the first request, it keeps only the redirectKeeps headers.
// The caller's own CheckRedirect then runs as usual.
func withRedirectPolicy(hc *http.Client) *http.Client {
	copied := *hc
	next := hc.CheckRedirect
	copied.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" && (req.URL.Scheme != "http" || !allowsCleartext(req.URL.Hostname())) {
			status := 0
			if req.Response != nil {
				status = req.Response.StatusCode
			}
			// The target is a presigned URL; its signature stays out of the error.
			return &Error{Kind: ErrRequest, Status: status, Code: "INSECURE_REDIRECT", Message: "refused a redirect to a URL that is not https"}
		}
		if leftOrigin(req, via) {
			for name := range req.Header {
				if !redirectKeeps[http.CanonicalHeaderKey(name)] {
					delete(req.Header, name)
				}
			}
		}
		if next != nil {
			return next(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &copied
}

// leftOrigin reports whether req, or any redirect before it, is on a scheme or
// host other than the first request's. net/http copies the first request's
// headers to every redirect, so a chain that comes back to the API origin is
// still stripped.
func leftOrigin(req *http.Request, via []*http.Request) bool {
	first := via[0].URL
	if req.URL.Scheme != first.Scheme || req.URL.Host != first.Host {
		return true
	}
	for _, prev := range via[1:] {
		if prev.URL.Scheme != first.Scheme || prev.URL.Host != first.Host {
			return true
		}
	}
	return false
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// request is one API call. body is JSON-encoded unless rawBody is set.
type request struct {
	method      string
	path        string
	query       url.Values
	body        any
	rawBody     []byte
	contentType string
	header      http.Header
	bearer      string
	idempotent  *bool
	timeout     time.Duration
	absoluteURL bool // path is a full URL (presigned storage); no API headers
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func boolPtr(b bool) *bool { return &b }

// isConnectFailure reports whether err happened before the request could have
// reached the server (dial / DNS failure), which makes any method safe to retry.
func isConnectFailure(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

// do sends req with timeouts and bounded retries and returns a 2xx response.
func (c *Client) do(ctx context.Context, req request) (*response, error) {
	var payload []byte
	contentType := req.contentType
	if req.rawBody != nil {
		payload = req.rawBody
	} else if req.body != nil {
		encoded, err := json.Marshal(req.body)
		if err != nil {
			return nil, invalidArg("encoding request body: %v", err)
		}
		payload = encoded
		if contentType == "" {
			contentType = "application/json"
		}
	}
	target := req.path
	if !req.absoluteURL {
		target = c.baseURL + req.path
	}
	if len(req.query) > 0 {
		target += "?" + req.query.Encode()
	}
	replaySafe := req.method == http.MethodGet || req.method == http.MethodHead || req.method == http.MethodOptions
	if req.idempotent != nil {
		replaySafe = *req.idempotent
	}
	timeout := c.timeout
	if req.timeout > 0 {
		timeout = req.timeout
	}
	policy := c.retry

	for attempt := 0; ; attempt++ {
		resp, err := c.attempt(ctx, req, target, payload, contentType, timeout)
		canRetry := attempt < policy.MaxRetries
		if err != nil {
			if ctx.Err() != nil { // the caller canceled or its deadline passed
				return nil, canceled(ctx.Err())
			}
			var sdkErr *Error
			if errors.As(err, &sdkErr) { // e.g. an oversized body: final, not transient
				return nil, sdkErr
			}
			retryable := policy.RetryOnNetworkError && (replaySafe || isConnectFailure(err))
			if canRetry && retryable {
				if serr := c.sleep(ctx, policy.delay(attempt, 0)); serr != nil {
					return nil, canceled(serr)
				}
				continue
			}
			if req.absoluteURL {
				return nil, objectStoreError(err, timeout)
			}
			return nil, transportError(err, target, timeout)
		}
		if resp.status >= 200 && resp.status < 300 {
			return resp, nil
		}
		apiErr := errorFromResponse(resp.status, resp.header, resp.body, http.StatusText(resp.status))
		if canRetry && replaySafe && policy.retriesStatus(resp.status) {
			if serr := c.sleep(ctx, policy.delay(attempt, apiErr.RetryAfter)); serr != nil {
				return nil, canceled(serr)
			}
			continue
		}
		return nil, apiErr
	}
}

func networkError(err error, timeout time.Duration) *Error {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return &Error{Kind: ErrTimeout, Message: fmt.Sprintf("request timed out after %s", timeout), Err: err}
	}
	return &Error{Kind: ErrNetwork, Message: "could not reach the VAIF API", Err: err}
}

// transportError wraps the transport failure of an API request sent to
// target. GET /storage/download answers with a 307 to a presigned
// object-store URL, and net/http reports a failure after that redirect with
// the presigned URL. So a failure on a scheme or host other than target's is
// reported by objectStoreError, which removes the signature.
func transportError(err error, target string, timeout time.Duration) *Error {
	if failedElsewhere(err, target) {
		return objectStoreError(err, timeout)
	}
	return networkError(err, timeout)
}

// failedElsewhere reports whether err is net/http's *url.Error for a URL on a
// scheme or host other than target's, which happens only after a redirect.
// A URL that cannot be parsed counts as elsewhere, so it is removed.
func failedElsewhere(err error, target string) bool {
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return false
	}
	first, terr := url.Parse(target)
	if terr != nil {
		return false
	}
	failed, ferr := url.Parse(urlErr.URL)
	if ferr != nil {
		return true
	}
	// A refused redirect reports the Location header as sent, which may be
	// relative to the request.
	failed = first.ResolveReference(failed)
	return failed.Scheme != first.Scheme || failed.Host != first.Host
}

// objectStoreError is networkError for a request to a signed object-store
// URL. The URL's query carries its signature, so it is removed from the
// *url.Error that net/http returns before the error is wrapped.
func objectStoreError(err error, timeout time.Duration) *Error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if u, perr := url.Parse(urlErr.URL); perr == nil {
			u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = "", false, "", ""
			urlErr.URL = u.String()
		} else {
			urlErr.URL = ""
		}
	}
	e := networkError(err, timeout)
	if e.Kind == ErrNetwork {
		e.Message = "could not reach the object store"
	}
	return e
}

func (c *Client) attempt(ctx context.Context, req request, target string, payload []byte, contentType string, timeout time.Duration) (*response, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	httpReq, err := http.NewRequestWithContext(attemptCtx, req.method, target, body)
	if err != nil {
		return nil, err
	}
	if req.absoluteURL {
		// Presigned object-store URL: send no API credentials.
		if contentType != "" {
			httpReq.Header.Set("Content-Type", contentType)
		}
	} else {
		for k, vs := range c.headers {
			for _, v := range vs {
				httpReq.Header.Add(k, v)
			}
		}
		httpReq.Header.Set("Accept", "application/json")
		httpReq.Header.Set("Accept-Versions", "v1")
		httpReq.Header.Set("User-Agent", "vaif-go/"+Version)
		for k, vs := range req.header {
			httpReq.Header.Del(k)
			for _, v := range vs {
				httpReq.Header.Add(k, v)
			}
		}
		if contentType != "" {
			httpReq.Header.Set("Content-Type", contentType)
		}
		if c.apiKey != "" { // a platform client has no project key
			httpReq.Header.Set(apiKeyHeader, c.apiKey)
		}
		if req.bearer != "" {
			httpReq.Header.Set("Authorization", "Bearer "+req.bearer)
		}
	}
	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	// Read one byte past the cap so an oversized body is an error, never a
	// silently truncated download.
	data, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxResponseBytes {
		return nil, &Error{
			Kind:    ErrBadResponse,
			Status:  httpResp.StatusCode,
			Message: fmt.Sprintf("response body is larger than %d MiB; download large objects through Storage.CreateSignedURL", maxResponseBytes>>20),
		}
	}
	return &response{status: httpResp.StatusCode, header: httpResp.Header, body: data}, nil
}

// functionRunHeader marks a response a deployed function produced. POST
// /functions/:name/invoke relays the function's own status code and sets this
// header on every response the function produced; the API sets it on no other
// response.
const functionRunHeader = "X-Vaif-Run-Id"

// send issues an API request as the signed-in user, if any. A 401 from the
// API on a request that carried a session token triggers one refresh and one
// retry: the API refused the token before running the handler, so the replay
// is safe. The retry carries the session the request carried, refreshed; when
// the user signed out or another session was set while the request was in
// flight, the 401 is returned instead. A 401 a function returned (it carries
// functionRunHeader) is the function's answer and is returned as it is: the
// function has already run.
func (c *Client) send(ctx context.Context, req request, requireSession bool) (*response, error) {
	bearer, generation, err := c.Auth.bearerFor(ctx)
	if err != nil {
		return nil, err
	}
	if requireSession && bearer == "" {
		return nil, &Error{Kind: ErrAuth, Code: "NOT_SIGNED_IN", Message: "not signed in"}
	}
	req.bearer = bearer
	resp, err := c.do(ctx, req)
	var apiErr *Error
	if err == nil || bearer == "" || !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized ||
		apiErr.header.Get(functionRunHeader) != "" {
		return resp, err
	}
	fresh, ok, rerr := c.Auth.replayToken(ctx, bearer, generation)
	if rerr != nil {
		return nil, rerr
	}
	if !ok {
		return nil, err
	}
	req.bearer = fresh
	return c.do(ctx, req)
}

func decodeJSON(resp *response, v any) error {
	if len(resp.body) == 0 {
		return &Error{Kind: ErrBadResponse, Status: resp.status, Message: "empty response body"}
	}
	if err := json.Unmarshal(resp.body, v); err != nil {
		return &Error{Kind: ErrBadResponse, Status: resp.status, Message: "response was not valid JSON", Err: err}
	}
	return nil
}

// canceled wraps the caller's context error; errors.Is(err, context.Canceled)
// and errors.Is(err, context.DeadlineExceeded) keep working.
func canceled(err error) *Error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Kind: ErrTimeout, Message: "context deadline exceeded", Err: err}
	}
	return &Error{Kind: ErrNetwork, Message: "request canceled", Err: err}
}

// NewPlatformClient creates a client for platform operations with an optional
// WithSession token. Platform signup and password-reset requests return
// CONSOLE_ONLY before HTTP. Use NewClient for application end users.
func NewPlatformClient(opts ...Option) (*Client, error) {
	c, err := NewClient("_platform", "_platform", opts...)
	if err != nil {
		return nil, err
	}
	c.projectID = ""
	c.apiKey = ""
	return c, nil
}
