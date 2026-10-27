package vaif

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// refreshRace is a client whose refresh request blocks until release is
// called, so a test can change the session while the request is in flight.
type refreshRace struct {
	client  *Client
	started chan struct{}
	once    sync.Once
	release chan struct{}
	mu      sync.Mutex
	events  []string
}

func newRefreshRace(t *testing.T) *refreshRace {
	t.Helper()
	r := &refreshRace{started: make(chan struct{}), release: make(chan struct{})}
	r.client = parityClient(t, func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/auth/refresh"):
			close(r.started)
			<-r.release
			return jsonResponse(`{"accessToken":"at_new","expiresIn":900}`), nil
		case strings.HasSuffix(req.URL.Path, "/auth/logout"):
			return jsonResponse(`{}`), nil
		}
		t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		return nil, errors.New("unexpected request")
	})
	t.Cleanup(r.unblock) // never leave the refresh goroutine parked
	if _, err := r.client.Auth.SetSession("at_old", "rt_old", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	r.client.Auth.OnSessionChange(func(event string, _ *Session) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.events = append(r.events, event)
	})
	return r
}

func (r *refreshRace) unblock() { r.once.Do(func() { close(r.release) }) }

// refresh starts RefreshSession and returns once its request is in flight.
func (r *refreshRace) refresh(t *testing.T) <-chan refreshResult {
	t.Helper()
	out := make(chan refreshResult, 1)
	go func() {
		s, err := r.client.Auth.RefreshSession(context.Background())
		out <- refreshResult{s, err}
	}()
	select {
	case <-r.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the refresh request never started")
	}
	return out
}

type refreshResult struct {
	session *Session
	err     error
}

func (r *refreshRace) wait(t *testing.T, out <-chan refreshResult) refreshResult {
	t.Helper()
	r.unblock()
	select {
	case res := <-out:
		return res
	case <-time.After(5 * time.Second):
		t.Fatal("RefreshSession never returned")
		return refreshResult{}
	}
}

func (r *refreshRace) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func eventsEqual(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestRefreshFinishingAfterSignOutDoesNotRestoreTheSession(t *testing.T) {
	r := newRefreshRace(t)
	out := r.refresh(t)
	if err := r.client.Auth.SignOut(context.Background()); err != nil {
		t.Fatal(err)
	}
	res := r.wait(t, out)

	var apiErr *Error
	if !errors.As(res.err, &apiErr) || !errors.Is(res.err, ErrAuth) || apiErr.Code != "NOT_SIGNED_IN" {
		t.Fatalf("RefreshSession = %+v, %v; want an auth error with code NOT_SIGNED_IN", res.session, res.err)
	}
	if res.session != nil {
		t.Fatalf("RefreshSession returned the session %+v after SignOut", res.session)
	}
	if s := r.client.Auth.Session(); s != nil {
		t.Fatalf("session after SignOut = %+v; want nil", s)
	}
	if got := r.seen(); !eventsEqual(got, "SIGNED_OUT") {
		t.Fatalf("events = %v; want [SIGNED_OUT] (no TOKEN_REFRESHED)", got)
	}
}

func TestRefreshFinishingAfterSetSessionReturnsTheNewerSession(t *testing.T) {
	r := newRefreshRace(t)
	out := r.refresh(t)
	if _, err := r.client.Auth.SetSession("at_other", "rt_other", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	res := r.wait(t, out)

	if res.err != nil {
		t.Fatalf("RefreshSession error = %v; want the session set meanwhile", res.err)
	}
	if res.session == nil || res.session.AccessToken != "at_other" || res.session.RefreshToken != "rt_other" {
		t.Fatalf("RefreshSession = %+v; want access token at_other and refresh token rt_other", res.session)
	}
	if s := r.client.Auth.Session(); s == nil || s.AccessToken != "at_other" {
		t.Fatalf("session = %+v; want at_other to stay", s)
	}
	if got := r.seen(); !eventsEqual(got, "SIGNED_IN") {
		t.Fatalf("events = %v; want [SIGNED_IN] from SetSession only (no TOKEN_REFRESHED)", got)
	}
}

// Guard: a refresh nothing interrupts still installs the new session.
func TestUninterruptedRefreshStillInstallsTheNewSession(t *testing.T) {
	r := newRefreshRace(t)
	out := r.refresh(t)
	res := r.wait(t, out)

	if res.err != nil || res.session == nil || res.session.AccessToken != "at_new" || res.session.RefreshToken != "rt_old" {
		t.Fatalf("RefreshSession = %+v, %v; want access token at_new and the unrotated refresh token rt_old", res.session, res.err)
	}
	if s := r.client.Auth.Session(); s == nil || s.AccessToken != "at_new" {
		t.Fatalf("session = %+v; want at_new", s)
	}
	if got := r.seen(); !eventsEqual(got, "TOKEN_REFRESHED") {
		t.Fatalf("events = %v; want [TOKEN_REFRESHED]", got)
	}
}

// A refused refresh token ends only the session the refresh read. A sign-in
// that lands while the request is in flight stays, with no SIGNED_OUT, and the
// check and the clear happen in one critical section: the hook runs once, with
// the lock held. A read-then-clear in two steps (the shape before 767fd916a)
// never reaches the guarded swap, so the hook does not run and this fails.
func TestRefusedRefreshClearsOnlyTheSessionItRead(t *testing.T) {
	var c *Client
	c = parityClient(t, func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/auth/refresh") {
			// A sign-in lands while the refresh request is in flight.
			if _, err := c.Auth.SetSession("at_other", "rt_other", time.Now().Add(time.Hour)); err != nil {
				t.Error(err)
			}
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"INVALID_REFRESH_TOKEN","message":"refresh token revoked"}}`)),
			}, nil
		}
		t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		return nil, errors.New("unexpected request")
	})
	if _, err := c.Auth.SetSession("at_old", "rt_old", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var events []string
	c.Auth.OnSessionChange(func(event string, _ *Session) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	})
	calls, held := 0, true
	c.Auth.testHookUnderLock = func() {
		calls++
		if c.Auth.mu.TryLock() {
			held = false
			c.Auth.mu.Unlock()
		}
	}

	_, err := c.Auth.RefreshSession(context.Background())
	var apiErr *Error
	if !errors.As(err, &apiErr) || !errors.Is(err, ErrAuth) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("RefreshSession error = %v; want the API's 401 as an auth error", err)
	}
	if s := c.Auth.Session(); s == nil || s.AccessToken != "at_other" || s.RefreshToken != "rt_other" {
		t.Fatalf("session = %+v; want at_other / rt_other, signed in during the refresh, to stay", s)
	}
	mu.Lock()
	got := append([]string(nil), events...)
	mu.Unlock()
	if !eventsEqual(got, "SIGNED_IN") {
		t.Fatalf("events = %v; want [SIGNED_IN] from SetSession only (no SIGNED_OUT)", got)
	}
	if calls != 1 || !held {
		t.Fatalf("guarded check: calls = %d, lock held = %v; want 1 call with the lock held", calls, held)
	}
}

// SignOut revokes on the server exactly the session it cleared: it reads and
// clears the session in one critical section (the hook runs once, with the
// lock held), so a sign-in landing between a separate read and clear can no
// longer be cleared locally while the older session's tokens are revoked.
func TestSignOutRevokesTheSessionItClears(t *testing.T) {
	var logout struct {
		auth string
		body string
	}
	c := parityClient(t, func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/auth/logout") {
			raw, _ := io.ReadAll(req.Body)
			logout.auth, logout.body = req.Header.Get("Authorization"), string(raw)
			return jsonResponse(`{}`), nil
		}
		t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		return nil, errors.New("unexpected request")
	})
	if _, err := c.Auth.SetSession("at_one", "rt_one", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var events []string
	c.Auth.OnSessionChange(func(event string, _ *Session) { events = append(events, event) })
	calls, held := 0, true
	c.Auth.testHookUnderLock = func() {
		calls++
		if c.Auth.mu.TryLock() {
			held = false
			c.Auth.mu.Unlock()
		}
	}

	if err := c.Auth.SignOut(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := c.Auth.Session(); s != nil {
		t.Fatalf("session after SignOut = %+v; want nil", s)
	}
	if !eventsEqual(events, "SIGNED_OUT") {
		t.Fatalf("events = %v; want [SIGNED_OUT]", events)
	}
	if logout.auth != "Bearer at_one" || logout.body != `{"refreshToken":"rt_one"}` {
		t.Fatalf("logout sent Authorization %q, body %q; want Bearer at_one and rt_one", logout.auth, logout.body)
	}
	if calls != 1 || !held {
		t.Fatalf("read and clear: calls = %d, lock held = %v; want 1 call with the lock held", calls, held)
	}
}

// A refresh in flight when SignOut runs may reach the server first and rotate
// the refresh token. SignOut's logout then names a token that no longer
// identifies a session, and once the access token has expired the server
// cannot find the user from it, so the rotated token would stay valid. The
// refresh revokes the token it received, with the access token it received,
// only when it was signed out meanwhile and the server sent a new token.
func TestRefreshFinishingAfterSignOutRevokesTheRotatedToken(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rotate  bool
		replace bool // SetSession instead of SignOut while the refresh is in flight
		want    []string
	}{
		{"signed out, rotated", true, false, []string{
			`Bearer at_old {"refreshToken":"rt_old"}`,
			`Bearer at_new {"refreshToken":"rt_rotated"}`,
		}},
		{"signed out, not rotated", false, false, []string{
			`Bearer at_old {"refreshToken":"rt_old"}`,
		}},
		{"replaced by another session, rotated", true, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var mu sync.Mutex
			var logouts []string
			c := parityClient(t, func(req *http.Request) (*http.Response, error) {
				switch {
				case strings.HasSuffix(req.URL.Path, "/auth/refresh"):
					close(started)
					<-release
					resp := jsonResponse(`{"accessToken":"at_new","expiresIn":900}`)
					if tc.rotate {
						resp.Header.Set("Set-Cookie", "project_refresh_token=rt_rotated; Path=/projects/project/auth; HttpOnly")
					}
					return resp, nil
				case strings.HasSuffix(req.URL.Path, "/auth/logout"):
					raw, _ := io.ReadAll(req.Body)
					mu.Lock()
					logouts = append(logouts, req.Header.Get("Authorization")+" "+string(raw))
					mu.Unlock()
					return jsonResponse(`{"ok":true}`), nil
				}
				t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
				return nil, errors.New("unexpected request")
			})
			if _, err := c.Auth.SetSession("at_old", "rt_old", time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			out := make(chan error, 1)
			go func() {
				_, err := c.Auth.RefreshSession(context.Background())
				out <- err
			}()
			<-started
			if tc.replace {
				if _, err := c.Auth.SetSession("at_other", "rt_other", time.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
			} else if err := c.Auth.SignOut(context.Background()); err != nil {
				t.Fatal(err)
			}
			close(release)
			select {
			case <-out:
			case <-time.After(5 * time.Second):
				t.Fatal("RefreshSession never returned")
			}
			mu.Lock()
			got := append([]string(nil), logouts...)
			mu.Unlock()
			if strings.Join(got, " | ") != strings.Join(tc.want, " | ") {
				t.Fatalf("logout requests = %q; want %q", got, tc.want)
			}
		})
	}
}
