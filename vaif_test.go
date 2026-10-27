package vaif_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	vaif "github.com/vaifstudio/vaif-go"
)

const (
	testProject = "proj_123"
	testKey     = "vk_pub_test"
)

func fastRetry() vaif.RetryPolicy {
	p := vaif.DefaultRetryPolicy()
	p.BaseDelay, p.MaxDelay = 0, 0
	return p
}

// fake runs HTTP handlers through a RoundTripper, without opening a port.
type fake struct {
	URL      string
	mu       sync.Mutex
	handler  http.HandlerFunc
	requests []*recorded
}

type recorded struct {
	Method string
	Path   string
	Query  map[string][]string
	Header http.Header
	Body   []byte
}

func newFake(t *testing.T, h http.HandlerFunc) *fake {
	t.Helper()
	f := &fake{handler: h}
	f.URL = "http://localhost"
	return f
}

type handlerTransport func(*http.Request) (*http.Response, error)

func (f handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func (f *fake) roundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	f.mu.Lock()
	f.requests = append(f.requests, &recorded{r.Method, r.URL.EscapedPath(), r.URL.Query(), r.Header.Clone(), body})
	handler := f.handler
	f.mu.Unlock()
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	handler(w, r)
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	return w.Result(), nil
}
func (f *fake) Close() {}

func (f *fake) calls() []*recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*recorded(nil), f.requests...)
}

func (f *fake) last() *recorded {
	c := f.calls()
	return c[len(c)-1]
}

func newClient(t *testing.T, f *fake, opts ...vaif.Option) *vaif.Client {
	t.Helper()
	all := append([]vaif.Option{vaif.WithBaseURL(f.URL), vaif.WithRetryPolicy(fastRetry()), vaif.WithHTTPClient(&http.Client{Transport: handlerTransport(f.roundTrip)})}, opts...)
	c, err := vaif.NewClient(testProject, testKey, all...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func listOK(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"data": []any{}, "count": 0})
}

func mustJSON(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("invalid JSON %q: %v", raw, err)
	}
	return v
}

func equalJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	g, _ := json.Marshal(mustJSON(t, got))
	w, _ := json.Marshal(mustJSON(t, []byte(want)))
	if string(g) != string(w) {
		t.Fatalf("JSON mismatch\n got: %s\nwant: %s", g, w)
	}
}

var ctx = context.Background()

// ---------------------------------------------------------------- client

func TestNewClientValidation(t *testing.T) {
	if _, err := vaif.NewClient("", testKey); !errors.Is(err, vaif.ErrInvalidArg) {
		t.Fatalf("empty project: %v", err)
	}
	if _, err := vaif.NewClient(testProject, ""); !errors.Is(err, vaif.ErrInvalidArg) {
		t.Fatalf("empty key: %v", err)
	}
	_, err := vaif.NewClient(testProject, testKey, vaif.WithBaseURL("http://api.vaif.studio"))
	var sdkErr *vaif.Error
	if !errors.As(err, &sdkErr) || !errors.Is(err, vaif.ErrInvalidArg) {
		t.Fatalf("plain http should be rejected: %v", err)
	}
	if sdkErr.Message != "base URL must use https (http is accepted only for localhost, 127.0.0.1 and ::1)" {
		t.Fatalf("Message = %q; want the three loopback hosts named", sdkErr.Message)
	}
	for base, want := range map[string]string{
		"http://localhost:3000/": "http://localhost:3000",
		"http://127.0.0.1:3000":  "http://127.0.0.1:3000",
		"http://[::1]:3000":      "http://[::1]:3000",
	} {
		c, err := vaif.NewClient(testProject, testKey, vaif.WithBaseURL(base))
		if err != nil || c.BaseURL() != want {
			t.Fatalf("WithBaseURL(%q): %v; want BaseURL %q", base, err, want)
		}
	}
}

// WithSession follows SetSession's rules: an empty access token is refused,
// a zero ExpiresAt means one hour from now, and the client keeps its own copy.
func TestWithSessionCopiesAndValidates(t *testing.T) {
	if _, err := vaif.NewClient(testProject, testKey, vaif.WithSession(&vaif.Session{})); !errors.Is(err, vaif.ErrInvalidArg) {
		t.Fatalf("WithSession(&Session{}) = %v; want ErrInvalidArg", err)
	}

	saved := &vaif.Session{AccessToken: "at_saved", RefreshToken: "rt_saved"}
	before := time.Now()
	c, err := vaif.NewClient(testProject, testKey, vaif.WithSession(saved))
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	saved.AccessToken, saved.RefreshToken = "at_changed", "rt_changed"

	got := c.Auth.Session()
	if got == nil || got.AccessToken != "at_saved" || got.RefreshToken != "rt_saved" {
		t.Fatalf("session = %+v; want at_saved / rt_saved, unaffected by later changes to the caller's struct", got)
	}
	if got.ExpiresAt.Before(before.Add(time.Hour)) || got.ExpiresAt.After(after.Add(time.Hour)) {
		t.Fatalf("ExpiresAt = %v; want one hour after NewClient", got.ExpiresAt)
	}
	if !saved.ExpiresAt.IsZero() {
		t.Fatal("NewClient changed the caller's Session")
	}
}

func TestSendsKeyVersionAndUserAgent(t *testing.T) {
	if vaif.Version != "1.0.0" {
		t.Fatalf("Version = %q, want %q", vaif.Version, "1.0.0")
	}
	f := newFake(t, listOK)
	c := newClient(t, f, vaif.WithHeader("X-App", "demo"))
	if _, err := c.From("todos").List(ctx); err != nil {
		t.Fatal(err)
	}
	h := f.last().Header
	for k, want := range map[string]string{
		"X-Vaif-Key":      testKey,
		"Accept-Versions": "v1",
		"User-Agent":      "vaif-go/1.0.0",
		"X-App":           "demo",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if h.Get("Authorization") != "" {
		t.Error("no session: Authorization must be absent")
	}
}

// ---------------------------------------------------------------- errors

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		body   map[string]any
		kind   error
		code   string
	}{
		{400, map[string]any{"error": "BadRequest", "code": "INVALID_COLUMN", "message": "no col"}, vaif.ErrValidation, "INVALID_COLUMN"},
		{401, map[string]any{"error": "INVALID_CREDENTIALS", "message": "bad"}, vaif.ErrAuth, "INVALID_CREDENTIALS"},
		{403, map[string]any{"error": "FORBIDDEN", "message": "nope"}, vaif.ErrAuth, "FORBIDDEN"},
		{404, map[string]any{"error": "NOT_FOUND", "message": "Record not found"}, vaif.ErrNotFound, "NOT_FOUND"},
		{409, map[string]any{"error": "CONFLICT", "message": "dupe"}, vaif.ErrConflict, "CONFLICT"},
		{422, map[string]any{"message": "bad entity"}, vaif.ErrValidation, ""},
		{402, map[string]any{"error": "PAYMENT_REQUIRED", "message": "upgrade"}, vaif.ErrRequest, "PAYMENT_REQUIRED"},
		{500, map[string]any{"error": "InternalError", "message": "boom"}, vaif.ErrServer, "InternalError"},
	}
	for _, tc := range cases {
		f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
			body := map[string]any{"requestId": "req_1"}
			for k, v := range tc.body {
				body[k] = v
			}
			writeJSON(w, tc.status, body)
		})
		c := newClient(t, f, vaif.WithRetryPolicy(vaif.NoRetry()))
		_, err := c.From("todos").List(ctx)
		var apiErr *vaif.Error
		if !errors.As(err, &apiErr) || !errors.Is(err, tc.kind) {
			t.Fatalf("%d: got %v", tc.status, err)
		}
		if apiErr.Status != tc.status || apiErr.Code != tc.code || apiErr.RequestID != "req_1" || apiErr.Message != tc.body["message"] {
			t.Fatalf("%d: got %+v", tc.status, apiErr)
		}
	}
}

// ---------------------------------------------------------------- retries

func TestRetriesGETOn5xxThenSucceeds(t *testing.T) {
	var n int32
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch atomic.AddInt32(&n, 1) {
		case 1:
			w.WriteHeader(503)
		case 2:
			w.WriteHeader(502)
		default:
			writeJSON(w, 200, map[string]any{"data": []any{map[string]any{"id": 1}}, "count": 1})
		}
	})
	res, err := newClient(t, f).From("todos").List(ctx)
	if err != nil || res.Count != 1 || len(f.calls()) != 3 {
		t.Fatalf("err=%v res=%+v calls=%d", err, res, len(f.calls()))
	}
}

func TestRetryBudgetIsBounded(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	_, err := newClient(t, f).From("todos").List(ctx)
	if !errors.Is(err, vaif.ErrServer) || len(f.calls()) != 4 {
		t.Fatalf("err=%v calls=%d", err, len(f.calls()))
	}
}

func TestDoesNotReplayPOSTOn5xx(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) })
	_, err := newClient(t, f).From("todos").Insert(ctx, vaif.Row{"title": "x"})
	if !errors.Is(err, vaif.ErrServer) || len(f.calls()) != 1 {
		t.Fatalf("err=%v calls=%d", err, len(f.calls()))
	}
}

// dialFailOnce fails the first round trip at dial time, then delegates.
type dialFailOnce struct {
	failed int32
	next   http.RoundTripper
}

func (d *dialFailOnce) RoundTrip(r *http.Request) (*http.Response, error) {
	if atomic.CompareAndSwapInt32(&d.failed, 0, 1) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	}
	return d.next.RoundTrip(r)
}

func TestRetriesAnyMethodWhenConnectionNeverOpened(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"data": map[string]any{"id": "n1"}})
	})
	hc := &http.Client{Transport: &dialFailOnce{next: handlerTransport(f.roundTrip)}}
	row, err := newClient(t, f, vaif.WithHTTPClient(hc)).From("todos").Insert(ctx, vaif.Row{"title": "x"})
	if err != nil || row["id"] != "n1" || len(f.calls()) != 1 {
		t.Fatalf("err=%v row=%v calls=%d", err, row, len(f.calls()))
	}
}

func TestTimeoutIsTypedAndBounded(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	})
	c := newClient(t, f, vaif.WithTimeout(50*time.Millisecond))
	start := time.Now()
	_, err := c.From("todos").Insert(ctx, vaif.Row{"title": "x"})
	if !errors.Is(err, vaif.ErrTimeout) || !errors.Is(err, vaif.ErrNetwork) {
		t.Fatalf("want timeout, got %v", err)
	}
	if len(f.calls()) != 1 || time.Since(start) > time.Second {
		t.Fatalf("POST timeout must not be replayed: calls=%d elapsed=%s", len(f.calls()), time.Since(start))
	}
	_, err = c.From("todos").List(ctx) // GET timeouts are retried
	if !errors.Is(err, vaif.ErrTimeout) || len(f.calls()) != 5 {
		t.Fatalf("err=%v calls=%d", err, len(f.calls()))
	}
}

func TestContextCancellation(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	c := newClient(t, f)
	cctx, cancel := context.WithCancel(ctx)
	time.AfterFunc(30*time.Millisecond, cancel)
	_, err := c.From("todos").List(cctx)
	var apiErr *vaif.Error
	if !errors.Is(err, context.Canceled) || !errors.As(err, &apiErr) || apiErr.Message != "request canceled" {
		t.Fatalf("want a *vaif.Error matching context.Canceled with message \"request canceled\", got %v", err)
	}
}

func TestRateLimitHonorsRetryAfter(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		writeJSON(w, 429, map[string]any{"error": "RateLimited", "message": "slow down"})
	})
	p := fastRetry()
	p.MaxRetries = 1
	_, err := newClient(t, f, vaif.WithRetryPolicy(p)).From("todos").List(ctx)
	var apiErr *vaif.Error
	if !errors.As(err, &apiErr) || !errors.Is(err, vaif.ErrRateLimited) || apiErr.RetryAfter != 7*time.Second {
		t.Fatalf("got %v", err)
	}
	if len(f.calls()) != 2 {
		t.Fatalf("calls=%d", len(f.calls()))
	}
}

// An object larger than the in-memory cap must fail loudly, not come back
// silently truncated, and must not be re-downloaded by the retry loop.
func TestOversizedBodyIsAnErrorNotATruncation(t *testing.T) {
	const capBytes = 64 << 20
	chunk := make([]byte, 1<<20)
	var hits atomic.Int32
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		for written := 0; written <= capBytes; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	c := newClient(t, f)
	data, err := c.Storage.Download(context.Background(), "big.bin", nil)
	if data != nil || !errors.Is(err, vaif.ErrBadResponse) {
		t.Fatalf("got %d bytes, err %v; want ErrBadResponse", len(data), err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("oversized body was fetched %d times, want 1", n)
	}
}
