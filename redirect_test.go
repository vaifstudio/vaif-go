package vaif_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	vaif "github.com/vaifstudio/vaif-go"
)

// redirectHosts serves an API host and an object-store host from one
// RoundTripper, the way GET /storage/download answers: a 307 to a presigned
// object-store URL. Storage.Download asks for the link instead; the generated
// GetStorageDownload calls this route and follows the redirect.
type redirectHosts struct {
	mu       sync.Mutex
	api      []http.Header
	store    []http.Header
	location string
	// storeLocation, when set, makes the object store answer with a 307 to
	// it instead of serving the object.
	storeLocation string
	// storeErr, when set, is the transport error every object-store request
	// fails with, as when the connection is reset.
	storeErr error
}

func (h *redirectHosts) roundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	h.mu.Lock()
	defer h.mu.Unlock()
	switch r.URL.Host {
	case "api.example.test":
		h.api = append(h.api, r.Header.Clone())
		switch r.URL.Path {
		case "/storage/download":
			w.Header().Set("Location", h.location)
			w.WriteHeader(http.StatusTemporaryRedirect)
		case "/storage/objects/a.txt":
			_, _ = w.Write([]byte("api-bytes"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	case "objects.example.test":
		h.store = append(h.store, r.Header.Clone())
		if h.storeErr != nil {
			return nil, h.storeErr
		}
		if h.storeLocation != "" {
			w.Header().Set("Location", h.storeLocation)
			w.WriteHeader(http.StatusTemporaryRedirect)
			break
		}
		_, _ = w.Write([]byte("file-bytes"))
	default:
		w.WriteHeader(http.StatusBadGateway)
	}
	return w.Result(), nil
}

func (h *redirectHosts) seen() (api, store []http.Header) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]http.Header(nil), h.api...), append([]http.Header(nil), h.store...)
}

func redirectClient(t *testing.T, h *redirectHosts, hc *http.Client) *vaif.Client {
	t.Helper()
	if hc == nil {
		hc = &http.Client{}
	}
	hc.Transport = handlerTransport(h.roundTrip)
	c, err := vaif.NewClient(testProject, "vk_srv_example",
		vaif.WithBaseURL("https://api.example.test"),
		vaif.WithRetryPolicy(vaif.NoRetry()),
		vaif.WithHeader("X-App", "demo"),
		vaif.WithHTTPClient(hc),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Auth.SetSession("at_user", "", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	return c
}

// downloadThroughRedirect calls GET /storage/download, which the API answers
// with a redirect.
func downloadThroughRedirect(c *vaif.Client) ([]byte, error) {
	return c.Operations.GetStorageDownload(ctx, vaif.OperationOptions{Query: url.Values{"key": {"a.txt"}}})
}

func TestDownloadRedirectSendsNoCredentialsToTheObjectStore(t *testing.T) {
	h := &redirectHosts{location: "https://objects.example.test/bucket/a.txt?X-Amz-Signature=sig"}
	c := redirectClient(t, h, nil)

	data, err := downloadThroughRedirect(c)
	if err != nil || string(data) != "file-bytes" {
		t.Fatalf("Download = %q, %v; want file-bytes", data, err)
	}
	api, store := h.seen()
	if len(api) != 1 || len(store) != 1 {
		t.Fatalf("requests: api=%d store=%d; want 1 and 1", len(api), len(store))
	}
	if api[0].Get("X-Vaif-Key") != "vk_srv_example" || api[0].Get("Authorization") != "Bearer at_user" || api[0].Get("X-App") != "demo" {
		t.Fatalf("API request headers = %v; want the key, the bearer token and X-App", api[0])
	}
	for _, name := range []string{"X-Vaif-Key", "Authorization", "X-App", "Cookie"} {
		if got := store[0].Get(name); got != "" {
			t.Errorf("object store received %s = %q; want none", name, got)
		}
	}
}

// Headers passed for one call (OperationOptions.Headers) are not sent to
// another origin either: a redirect that leaves the API keeps only Accept,
// Accept-Encoding, Accept-Language, Content-Type, Range and User-Agent.
func TestRedirectToAnotherOriginKeepsOnlyNeutralHeaders(t *testing.T) {
	h := &redirectHosts{location: "https://objects.example.test/bucket/a.txt?X-Amz-Signature=sig"}
	c := redirectClient(t, h, nil)

	_, err := c.Operations.GetStorageDownload(ctx, vaif.OperationOptions{
		Query:   url.Values{"key": {"a.txt"}},
		Headers: http.Header{"X-Vaif-Org": {"org_1"}, "Apikey": {"k_1"}, "Range": {"bytes=0-3"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, store := h.seen()
	if len(store) != 1 {
		t.Fatalf("object store requests = %d; want 1", len(store))
	}
	for _, name := range []string{"X-Vaif-Org", "Apikey", "Accept-Versions", "X-Vaif-Key", "Authorization", "X-App"} {
		if got := store[0].Get(name); got != "" {
			t.Errorf("object store received %s = %q; want none", name, got)
		}
	}
	if store[0].Get("Range") != "bytes=0-3" || store[0].Get("User-Agent") != "vaif-go/1.0.0" {
		t.Errorf("object store headers = %v; want Range bytes=0-3 and User-Agent vaif-go/1.0.0 kept", store[0])
	}
}

func TestRedirectToPlainHTTPIsRefused(t *testing.T) {
	h := &redirectHosts{location: "http://objects.example.test/bucket/a.txt?X-Amz-Signature=sig"}
	c := redirectClient(t, h, nil)

	_, err := downloadThroughRedirect(c)
	var apiErr *vaif.Error
	if !errors.As(err, &apiErr) || !errors.Is(err, vaif.ErrRequest) || apiErr.Code != "INSECURE_REDIRECT" || apiErr.Status != 307 {
		t.Fatalf("Download error = %v; want ErrRequest, code INSECURE_REDIRECT, status 307", err)
	}
	if strings.Contains(err.Error(), "Signature") {
		t.Fatalf("error text %q carries the presigned URL", err.Error())
	}
	if _, store := h.seen(); len(store) != 0 {
		t.Fatalf("object store received %d requests over plain http; want 0", len(store))
	}
}

// Rule 5: a chain that leaves the API origin and comes back to it (API, then
// object store, then API) does not carry credentials on the last hop either.
// net/http copies the first request's headers to every redirect, so without
// this an object store could send a request with the project key and the
// bearer token to any path on the API.
func TestRedirectBackToTheAPIAfterLeavingItCarriesNoCredentials(t *testing.T) {
	h := &redirectHosts{
		location:      "https://objects.example.test/bucket/a.txt?X-Amz-Signature=sig",
		storeLocation: "https://api.example.test/storage/objects/a.txt",
	}
	c := redirectClient(t, h, nil)

	data, err := downloadThroughRedirect(c)
	if err != nil || string(data) != "api-bytes" {
		t.Fatalf("Download = %q, %v; want api-bytes", data, err)
	}
	api, store := h.seen()
	if len(api) != 2 || len(store) != 1 {
		t.Fatalf("requests: api=%d store=%d; want 2 and 1", len(api), len(store))
	}
	if api[0].Get("X-Vaif-Key") != "vk_srv_example" || api[0].Get("Authorization") != "Bearer at_user" {
		t.Fatalf("first API request headers = %v; want the key and the bearer token", api[0])
	}
	for _, name := range []string{"X-Vaif-Key", "Authorization", "X-App"} {
		if got := api[1].Get(name); got != "" {
			t.Errorf("API request after the object store carried %s = %q; want none", name, got)
		}
	}
}

// Guard: a redirect that stays on the API origin keeps the API headers.
func TestRedirectWithinTheAPIKeepsCredentials(t *testing.T) {
	h := &redirectHosts{location: "https://api.example.test/storage/objects/a.txt"}
	c := redirectClient(t, h, nil)

	data, err := downloadThroughRedirect(c)
	if err != nil || string(data) != "api-bytes" {
		t.Fatalf("Download = %q, %v; want api-bytes", data, err)
	}
	api, _ := h.seen()
	if len(api) != 2 || api[1].Get("X-Vaif-Key") != "vk_srv_example" || api[1].Get("X-App") != "demo" {
		t.Fatalf("API requests = %v; want 2, the second with the key and X-App", api)
	}
}

// Guard: the caller's own redirect policy still applies, and the caller's
// *http.Client is not changed.
func TestCallerRedirectPolicyStillApplies(t *testing.T) {
	h := &redirectHosts{location: "https://objects.example.test/bucket/a.txt?X-Amz-Signature=sig"}
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	c := redirectClient(t, h, hc)

	_, err := downloadThroughRedirect(c)
	var apiErr *vaif.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 307 {
		t.Fatalf("Download error = %v; want the 307 itself", err)
	}
	if _, store := h.seen(); len(store) != 0 {
		t.Fatalf("object store received %d requests; want 0", len(store))
	}

	plain := &http.Client{Transport: handlerTransport(h.roundTrip)}
	if _, err := vaif.NewClient(testProject, testKey, vaif.WithHTTPClient(plain)); err != nil {
		t.Fatal(err)
	}
	if plain.CheckRedirect != nil {
		t.Fatal("NewClient set CheckRedirect on the caller's *http.Client")
	}
}

// chainText returns the text of err and of every error it wraps.
func chainText(err error) []string {
	var out []string
	for queue := []error{err}; len(queue) > 0; queue = queue[1:] {
		e := queue[0]
		if e == nil {
			continue
		}
		out = append(out, e.Error())
		switch u := e.(type) {
		case interface{ Unwrap() error }:
			queue = append(queue, u.Unwrap())
		case interface{ Unwrap() []error }:
			queue = append(queue, u.Unwrap()...)
		}
	}
	return out
}

// A network failure at the object store after the API's 307 keeps the
// presigned URL's signature out of every error in the chain, and the message
// names the object store: the API was reached.
func TestObjectStoreFailureAfterARedirectHidesTheSignature(t *testing.T) {
	h := &redirectHosts{
		location: "https://objects.example.test/a.txt?X-Amz-Signature=secret",
		storeErr: errors.New("connection reset by peer"),
	}
	c := redirectClient(t, h, nil)

	_, err := downloadThroughRedirect(c)
	for _, text := range chainText(err) {
		if strings.Contains(text, "secret") {
			t.Errorf("error chain carries the signature: %q", text)
		}
	}
	var apiErr *vaif.Error
	if !errors.As(err, &apiErr) || !errors.Is(err, vaif.ErrNetwork) || apiErr.Message != "could not reach the object store" {
		t.Fatalf("Download error = %v; want ErrNetwork with message %q", err, "could not reach the object store")
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Fatalf("Download error = %v; want a wrapped *url.Error", err)
	}
	if urlErr.URL != "https://objects.example.test/a.txt" {
		t.Errorf("url.Error URL = %q; want https://objects.example.test/a.txt", urlErr.URL)
	}
	if _, store := h.seen(); len(store) != 1 {
		t.Fatalf("object store requests = %d; want 1", len(store))
	}
}

// The event-stream transport scrubs the same way when a redirect it followed
// to another origin fails.
func TestStreamFailureAfterARedirectHidesTheSignature(t *testing.T) {
	hc := &http.Client{Transport: handlerTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "objects.example.test" {
			return nil, errors.New("connection reset by peer")
		}
		w := httptest.NewRecorder()
		w.Header().Set("Location", "https://objects.example.test/stream?X-Amz-Signature=secret")
		w.WriteHeader(http.StatusTemporaryRedirect)
		return w.Result(), nil
	})}
	c, err := vaif.NewClient(testProject, testKey, vaif.WithBaseURL("https://api.example.test"), vaif.WithHTTPClient(hc))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Operations.PostAiCopilotChatStream(ctx, vaif.OperationOptions{})
	for _, text := range chainText(err) {
		if strings.Contains(text, "secret") {
			t.Errorf("error chain carries the signature: %q", text)
		}
	}
	var apiErr *vaif.Error
	if !errors.As(err, &apiErr) || !errors.Is(err, vaif.ErrNetwork) || apiErr.Message != "could not reach the object store" {
		t.Fatalf("stream error = %v; want ErrNetwork with message %q", err, "could not reach the object store")
	}
}

// Guard: a failure at the API origin itself keeps the API message and URL.
func TestAPIFailureKeepsTheAPIMessage(t *testing.T) {
	hc := &http.Client{Transport: handlerTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection reset by peer")
	})}
	c, err := vaif.NewClient(testProject, testKey, vaif.WithBaseURL("https://api.example.test"),
		vaif.WithRetryPolicy(vaif.NoRetry()), vaif.WithHTTPClient(hc))
	if err != nil {
		t.Fatal(err)
	}
	_, err = downloadThroughRedirect(c)
	var apiErr *vaif.Error
	if !errors.As(err, &apiErr) || !errors.Is(err, vaif.ErrNetwork) || apiErr.Message != "could not reach the VAIF API" {
		t.Fatalf("Download error = %v; want ErrNetwork with message %q", err, "could not reach the VAIF API")
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) || urlErr.URL != "https://api.example.test/storage/download?key=a.txt" {
		t.Fatalf("Download error = %v; want a *url.Error for https://api.example.test/storage/download?key=a.txt", err)
	}
}

// The event-stream transport reports a refused redirect the same way.
func TestStreamOperationReportsARefusedRedirect(t *testing.T) {
	var requests int
	hc := &http.Client{Transport: handlerTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		w := httptest.NewRecorder()
		w.Header().Set("Location", "http://elsewhere.example.test/stream")
		w.WriteHeader(http.StatusTemporaryRedirect)
		return w.Result(), nil
	})}
	c, err := vaif.NewClient(testProject, testKey, vaif.WithBaseURL("https://api.example.test"), vaif.WithHTTPClient(hc))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Operations.PostAiCopilotChatStream(ctx, vaif.OperationOptions{})
	var apiErr *vaif.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "INSECURE_REDIRECT" || !errors.Is(err, vaif.ErrRequest) {
		t.Fatalf("stream error = %v; want ErrRequest with code INSECURE_REDIRECT", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d; want 1", requests)
	}
}
