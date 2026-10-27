package vaif

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// realtimeTarget runs Subscribe on a client whose base URL is baseURL and
// reports the URL it dialed, how many sockets it opened and how many HTTP
// requests it made. The client holds an access token that is about to expire,
// so reading the token would call the refresh route: requests > 0 means the
// token was read. baseURL is set directly because NewClient would reject the
// unsafe values this test needs.
func realtimeTarget(t *testing.T, baseURL string) (target string, dials, requests int, err error) {
	t.Helper()
	c := parityClient(t, func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("no network in this test")
	})
	if _, err := c.Auth.SetSession("at", "rt", time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	c.baseURL = baseURL
	f := &fakeSocket{frames: []RealtimeEvent{{Type: "welcome"}, {Type: "subscribed", Channel: "table:notes"}}}
	c.Realtime.dial = func(_ context.Context, u string) (realtimeSocket, error) {
		dials++
		target = u
		return f, nil
	}
	sub, err := c.Realtime.Subscribe(context.Background(), "notes", nil)
	if sub != nil {
		sub.Close()
	}
	return target, dials, requests, err
}

func TestRealtimeURLHTTPSBecomesWSS(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.example.com":         "wss://api.example.com/realtime/ws",
		"HTTPS://api.example.com":         "wss://api.example.com/realtime/ws",
		"Https://api.example.com:8443/v2": "wss://api.example.com:8443/v2/realtime/ws",
	} {
		got, err := realtimeURL(base)
		if err != nil || got != want {
			t.Errorf("realtimeURL(%q) = %q, %v; want %q", base, got, err, want)
		}
	}
}

func TestRealtimeURLHTTPOnlyForLoopback(t *testing.T) {
	for base, want := range map[string]string{
		"http://localhost:3147":  "ws://localhost:3147/realtime/ws",
		"HTTP://localhost:3147":  "ws://localhost:3147/realtime/ws",
		"http://127.0.0.1:3147":  "ws://127.0.0.1:3147/realtime/ws",
		"http://[::1]:3147":      "ws://[::1]:3147/realtime/ws",
		"http://localhost:3147/": "ws://localhost:3147/realtime/ws",
	} {
		got, err := realtimeURL(base)
		if err != nil || got != want {
			t.Errorf("realtimeURL(%q) = %q, %v; want %q", base, got, err, want)
		}
	}
}

func TestRealtimeURLRefusesEverythingElse(t *testing.T) {
	for _, base := range []string{
		"http://api.example.com",
		"HTTP://api.example.com",
		"http://localhost.example.com",
		"http://192.168.1.10:3147",
		"ftp://x",
		"ws://localhost:3147",
		"wss://api.example.com",
		"api.example.com",
		"https://",
		"",
	} {
		got, err := realtimeURL(base)
		var apiErr *Error
		if !errors.As(err, &apiErr) || !errors.Is(err, ErrInvalidArg) || apiErr.Code != "INVALID_REALTIME_URL" || got != "" {
			t.Errorf("realtimeURL(%q) = %q, %v; want INVALID_REALTIME_URL", base, got, err)
		}
	}
}

// Subscribe must refuse before it reads the token or opens a socket.
func TestRealtimeSubscribeRefusesCleartextBeforeToken(t *testing.T) {
	for _, base := range []string{"http://api.example.com", "ftp://x", "ws://api.example.com"} {
		target, dials, requests, err := realtimeTarget(t, base)
		var apiErr *Error
		if !errors.As(err, &apiErr) || !errors.Is(err, ErrInvalidArg) || apiErr.Code != "INVALID_REALTIME_URL" {
			t.Errorf("%s: err = %v; want INVALID_REALTIME_URL", base, err)
		}
		if dials != 0 || target != "" {
			t.Errorf("%s: dialed %q (%d sockets); want none", base, target, dials)
		}
		if requests != 0 {
			t.Errorf("%s: %d requests before the refusal; want 0 (the token must not be read)", base, requests)
		}
	}
}

// A client on HTTPS in capitals dials wss, through Subscribe.
func TestRealtimeSubscribeDialsWSSForUppercaseHTTPS(t *testing.T) {
	c := parityClient(t, nil)
	c.baseURL = "HTTPS://api.example.com"
	f := &fakeSocket{frames: []RealtimeEvent{{Type: "welcome"}, {Type: "subscribed", Channel: "table:notes"}}}
	var target string
	c.Realtime.dial = func(_ context.Context, u string) (realtimeSocket, error) {
		target = u
		return f, nil
	}
	sub, err := c.Realtime.Subscribe(context.Background(), "notes", nil)
	if err != nil {
		t.Fatal(err)
	}
	sub.Close()
	if target != "wss://api.example.com/realtime/ws" {
		t.Fatalf("dialed %q; want wss://api.example.com/realtime/ws", target)
	}
}
