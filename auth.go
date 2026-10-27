package vaif

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	refreshCookie = "project_refresh_token"
	// refreshLeeway is how long before expiry a token is refreshed.
	refreshLeeway = 30 * time.Second
)

// Session change events passed to OnSessionChange listeners.
const (
	EventSignedIn       = "SIGNED_IN"
	EventTokenRefreshed = "TOKEN_REFRESHED"
	EventSignedOut      = "SIGNED_OUT"
)

// SessionListener is called with an event name and the new session (nil when
// signed out). It runs synchronously; do not call back into AuthClient from it.
type SessionListener func(event string, session *Session)

// AuthClient signs a project's end users in and out (/projects/:id/auth).
// Sessions live in memory; use OnSessionChange to persist them and
// SetSession to restore one.
type AuthClient struct {
	client *Client

	mu        sync.Mutex
	session   *Session
	inflight  *refreshCall
	listeners map[int]SessionListener
	nextID    int
	// generation changes whenever the session is replaced other than by a
	// refresh of it: a sign-in, SetSession, SignOut or a refused refresh. Two
	// reads with the same generation saw the same session, or the later one
	// saw a session refreshed from the earlier one.
	generation uint64

	// testHookUnderLock, when a test sets it, runs with mu held at the two
	// places that read the session and act on it in one critical section: a
	// guarded swap (setIfCurrent) before it compares, and take before it
	// removes the session.
	testHookUnderLock func()
}

type refreshCall struct {
	done    chan struct{}
	session *Session
	err     error
}

// Session returns a copy of the current session, or nil when signed out.
func (a *AuthClient) Session() *Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session == nil {
		return nil
	}
	s := *a.session
	return &s
}

// OnSessionChange registers listener and returns a function that removes it.
func (a *AuthClient) OnSessionChange(listener SessionListener) (unsubscribe func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.listeners == nil {
		a.listeners = map[int]SessionListener{}
	}
	id := a.nextID
	a.nextID++
	a.listeners[id] = listener
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(a.listeners, id)
	}
}

// SetSession restores a persisted session. A zero expiresAt is treated as one
// hour from now; the SDK then relies on 401-triggered refresh.
func (a *AuthClient) SetSession(accessToken, refreshToken string, expiresAt time.Time) (*Session, error) {
	if accessToken == "" {
		return nil, invalidArg("accessToken must not be empty")
	}
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(time.Hour)
	}
	s := &Session{AccessToken: accessToken, RefreshToken: refreshToken, ExpiresAt: expiresAt}
	a.set(s, EventSignedIn)
	return s, nil
}

func (a *AuthClient) set(s *Session, event string) { a.swap(s, event, false, nil) }

// setIfCurrent is set, but only while the session is still from, the one the
// caller read before a network call. It reports whether s was installed; if
// not, nothing changed and now is the session in place (nil: signed out). The
// check and the write happen under one lock, so a sign-out cannot slip between.
func (a *AuthClient) setIfCurrent(from, s *Session, event string) (now *Session, installed bool) {
	return a.swap(s, event, true, from)
}

func (a *AuthClient) swap(s *Session, event string, guarded bool, from *Session) (*Session, bool) {
	a.mu.Lock()
	if guarded && a.testHookUnderLock != nil {
		a.testHookUnderLock()
	}
	if guarded && a.session != from {
		var now *Session
		if a.session != nil {
			c := *a.session
			now = &c
		}
		a.mu.Unlock()
		return now, false
	}
	if s != nil {
		stored := *s // callers may mutate what we return; keep our own copy
		a.session = &stored
	} else {
		a.session = nil
	}
	if event != EventTokenRefreshed {
		a.generation++
	}
	listeners := a.listenersLocked()
	a.mu.Unlock()
	notify(listeners, event, s)
	return s, true
}

// take removes the session and returns it (nil when signed out), reading and
// clearing it under one lock, so the caller acts on exactly the session it
// cleared. Listeners get SIGNED_OUT when there was a session.
func (a *AuthClient) take() *Session {
	a.mu.Lock()
	if a.testHookUnderLock != nil {
		a.testHookUnderLock()
	}
	current := a.session
	if current == nil {
		a.mu.Unlock()
		return nil
	}
	a.session = nil
	a.generation++
	listeners := a.listenersLocked()
	a.mu.Unlock()
	notify(listeners, EventSignedOut, nil)
	return current
}

// listenersLocked snapshots the listeners; mu must be held.
func (a *AuthClient) listenersLocked() []SessionListener {
	listeners := make([]SessionListener, 0, len(a.listeners))
	for _, l := range a.listeners {
		listeners = append(listeners, l)
	}
	return listeners
}

// notify calls each listener with its own copy of s, outside the lock.
func notify(listeners []SessionListener, event string, s *Session) {
	for _, l := range listeners {
		var copyS *Session
		if s != nil {
			c := *s
			copyS = &c
		}
		l(event, copyS)
	}
}

func (a *AuthClient) path(suffix string) string {
	if a.client.projectID == "" {
		return "/auth/" + suffix
	}
	return "/projects/" + url.PathEscape(a.client.projectID) + "/auth/" + suffix
}

// refreshTokenFrom reads the rotating refresh token from Set-Cookie. The API
// sends it only as an HttpOnly cookie, so a native client reads it here and
// sends it back explicitly on refresh.
func refreshTokenFrom(h http.Header) string {
	for _, line := range h.Values("Set-Cookie") {
		name, rest, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) != refreshCookie {
			continue
		}
		value, _, _ := strings.Cut(rest, ";")
		if unescaped, err := url.QueryUnescape(strings.TrimSpace(value)); err == nil {
			return unescaped
		}
		return strings.TrimSpace(value)
	}
	return ""
}

type tokenBody struct {
	AccessToken         string  `json:"accessToken"`
	ExpiresIn           float64 `json:"expiresIn"`
	User                *User   `json:"user"`
	MFARequired         bool    `json:"mfaRequired"`
	ChallengeToken      string  `json:"challengeToken"`
	PendingVerification *bool   `json:"pendingVerification"`
	Message             string  `json:"message"`
}

func sessionFrom(body *tokenBody, refreshToken string, status int) (*Session, error) {
	if body.AccessToken == "" {
		return nil, &Error{Kind: ErrBadResponse, Status: status, Message: "response did not include an access token"}
	}
	lifetime := body.ExpiresIn
	if lifetime <= 0 {
		lifetime = 900
	}
	return &Session{
		AccessToken:  body.AccessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(lifetime * float64(time.Second))),
		User:         body.User,
	}, nil
}

// SignUpOptions are optional sign-up fields.
type SignUpOptions struct {
	Name        string
	RedirectURL string
	Metadata    map[string]any
	Phone       string
}

// SignUp creates an account with email and password.
func (a *AuthClient) SignUp(ctx context.Context, email, password string, opts *SignUpOptions) (*SignUpResult, error) {
	if a.client.projectID == "" {
		return nil, consoleOnlySignUp("Auth.SignUp()", ownAppClient)
	}
	payload := map[string]any{"email": email, "password": password}
	if opts != nil {
		if opts.Name != "" {
			payload["name"] = opts.Name
		}
		if opts.RedirectURL != "" {
			payload["redirectUrl"] = opts.RedirectURL
		}
		if opts.Metadata != nil {
			payload["metadata"] = opts.Metadata
		}
		if opts.Phone != "" {
			payload["phone"] = opts.Phone
		}
	}
	resp, err := a.client.do(ctx, request{method: http.MethodPost, path: a.path("signup"), body: payload})
	if err != nil {
		return nil, err
	}
	var body tokenBody
	if err := decodeJSON(resp, &body); err != nil {
		return nil, err
	}
	if body.AccessToken != "" {
		s, err := sessionFrom(&body, refreshTokenFrom(resp.header), resp.status)
		if err != nil {
			return nil, err
		}
		a.set(s, EventSignedIn)
		return &SignUpResult{Session: s, User: s.User}, nil
	}
	pending := body.PendingVerification == nil || *body.PendingVerification
	return &SignUpResult{User: body.User, PendingVerification: pending, Message: body.Message}, nil
}

// SignIn signs in with email and password. When MFA is enrolled the result has
// MFARequired and a ChallengeToken; finish with VerifyMFA.
func (a *AuthClient) SignIn(ctx context.Context, email, password string) (*SignInResult, error) {
	resp, err := a.client.do(ctx, request{
		method: http.MethodPost,
		path:   a.path("login"),
		body:   map[string]any{"email": email, "password": password},
	})
	if err != nil {
		return nil, err
	}
	var body tokenBody
	if err := decodeJSON(resp, &body); err != nil {
		return nil, err
	}
	if body.MFARequired {
		return &SignInResult{MFARequired: true, ChallengeToken: body.ChallengeToken}, nil
	}
	s, err := sessionFrom(&body, refreshTokenFrom(resp.header), resp.status)
	if err != nil {
		return nil, err
	}
	a.set(s, EventSignedIn)
	return &SignInResult{Session: s}, nil
}

// VerifyMFA completes an MFA sign-in with a TOTP or backup code.
func (a *AuthClient) VerifyMFA(ctx context.Context, challengeToken, code string) (*Session, error) {
	resp, err := a.client.do(ctx, request{
		method: http.MethodPost,
		path:   a.path("mfa/verify"),
		body:   map[string]any{"challengeToken": challengeToken, "code": code},
	})
	if err != nil {
		return nil, err
	}
	var body tokenBody
	if err := decodeJSON(resp, &body); err != nil {
		return nil, err
	}
	s, err := sessionFrom(&body, refreshTokenFrom(resp.header), resp.status)
	if err != nil {
		return nil, err
	}
	a.set(s, EventSignedIn)
	return s, nil
}

// RefreshSession exchanges the refresh token for a new access token.
// Concurrent callers share one in-flight refresh (single flight); a caller
// whose ctx ends stops waiting but does not cancel the refresh for others.
func (a *AuthClient) RefreshSession(ctx context.Context) (*Session, error) {
	a.mu.Lock()
	call := a.inflight
	if call == nil {
		call = &refreshCall{done: make(chan struct{})}
		a.inflight = call
		current := a.session
		a.mu.Unlock()
		go a.runRefresh(context.WithoutCancel(ctx), call, current)
	} else {
		a.mu.Unlock()
	}
	select {
	case <-call.done:
		if call.err != nil {
			return nil, call.err
		}
		s := *call.session
		return &s, nil
	case <-ctx.Done():
		return nil, canceled(ctx.Err())
	}
}

func (a *AuthClient) runRefresh(ctx context.Context, call *refreshCall, current *Session) {
	defer func() {
		a.mu.Lock()
		a.inflight = nil
		a.mu.Unlock()
		close(call.done)
	}()
	if current == nil || current.RefreshToken == "" {
		call.err = &Error{Kind: ErrAuth, Code: "NO_REFRESH_TOKEN", Message: "no refresh token available; sign in again"}
		return
	}
	header := http.Header{}
	header.Set("Cookie", refreshCookie+"="+current.RefreshToken)
	resp, err := a.client.do(ctx, request{method: http.MethodPost, path: a.path("refresh"), header: header})
	if err != nil {
		var apiErr *Error
		if errors.As(err, &apiErr) && errors.Is(apiErr, ErrAuth) {
			// Revoked or expired refresh token: the session is over, unless
			// SignOut, SetSession or a sign-in replaced it while the request
			// was in flight. setIfCurrent compares and clears under one lock.
			a.setIfCurrent(current, nil, EventSignedOut)
		}
		call.err = err
		return
	}
	var body tokenBody
	if err := decodeJSON(resp, &body); err != nil {
		call.err = err
		return
	}
	rotated := refreshTokenFrom(resp.header)
	if rotated == "" {
		rotated = current.RefreshToken
	}
	s, err := sessionFrom(&body, rotated, resp.status)
	if err != nil {
		call.err = err
		return
	}
	if s.User == nil {
		s.User = current.User
	}
	// SignOut, SetSession or a sign-in may have run while the request was in
	// flight. This result must not bring the old session back or replace a
	// newer one: signed out -> NOT_SIGNED_IN; replaced -> the newer session.
	now, installed := a.setIfCurrent(current, s, EventTokenRefreshed)
	switch {
	case installed:
		call.session = s
	case now != nil:
		call.session = now
	default:
		call.err = &Error{Kind: ErrAuth, Code: "NOT_SIGNED_IN", Message: "signed out while the session was being refreshed"}
		// If the refresh reached the server before SignOut's logout, the
		// server rotated the refresh token, SignOut's token no longer names
		// a session, and the rotated one would stay valid. Revoke it with the
		// access token this refresh received. Best effort: the caller already
		// signed out, so a failure here is not reported.
		if fresh := refreshTokenFrom(resp.header); fresh != "" {
			_, _ = a.client.do(ctx, request{method: http.MethodPost, path: a.path("logout"), bearer: s.AccessToken, body: map[string]any{"refreshToken": fresh}})
		}
	}
}

// SignOut clears the session locally, then asks the server to revoke it. The
// server signs the user out of every session. The request carries the access
// token and the refresh token, so the server can find the user after the
// access token has expired. The local session is cleared even if the request
// fails; a 401 is not reported as an error. The session revoked is the one
// cleared: a sign-in that lands during SignOut is either cleared and revoked,
// or kept. A refresh that finishes after SignOut revokes the refresh token it
// received.
func (a *AuthClient) SignOut(ctx context.Context) error {
	current := a.take()
	if current == nil {
		return nil
	}
	body := map[string]any{}
	if current.RefreshToken != "" {
		body["refreshToken"] = current.RefreshToken
	}
	_, err := a.client.do(ctx, request{method: http.MethodPost, path: a.path("logout"), bearer: current.AccessToken, body: body})
	var apiErr *Error
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized {
		return nil
	}
	return err
}

// GetUser fetches the signed-in user's profile.
func (a *AuthClient) GetUser(ctx context.Context) (*User, error) {
	resp, err := a.client.send(ctx, request{method: http.MethodGet, path: a.path("me")}, true)
	if err != nil {
		return nil, err
	}
	var body struct {
		User *User `json:"user"`
	}
	if err := decodeJSON(resp, &body); err != nil {
		return nil, err
	}
	if body.User == nil {
		return nil, &Error{Kind: ErrBadResponse, Status: resp.status, Message: "response did not include a user"}
	}
	return body.User, nil
}

// RequestPasswordReset emails a reset link. It succeeds whether or not the
// address has an account.
func (a *AuthClient) RequestPasswordReset(ctx context.Context, email string) error {
	if a.client.projectID == "" {
		return consoleOnlyPasswordReset("Auth.RequestPasswordReset()", ownAppClient)
	}
	_, err := a.client.do(ctx, request{method: http.MethodPost, path: a.path("forgot-password"), body: map[string]any{"email": email}})
	return err
}

// ResetPassword sets a new password using the token from the reset email.
func (a *AuthClient) ResetPassword(ctx context.Context, token, newPassword string) error {
	_, err := a.client.do(ctx, request{
		method: http.MethodPost,
		path:   a.path("reset-password"),
		body:   map[string]any{"token": token, "password": newPassword},
	})
	return err
}

// bearer returns the access token for an outgoing request, refreshing first
// when it is about to expire.
func (a *AuthClient) bearer(ctx context.Context) (string, error) {
	token, _, err := a.bearerFor(ctx)
	return token, err
}

// bearerFor is bearer, and also returns the generation of the session it
// read, for replayToken.
func (a *AuthClient) bearerFor(ctx context.Context) (string, uint64, error) {
	a.mu.Lock()
	s, generation := a.session, a.generation
	a.mu.Unlock()
	if s == nil {
		return "", generation, nil
	}
	if s.RefreshToken != "" && s.ExpiresWithin(refreshLeeway) {
		fresh, err := a.RefreshSession(ctx)
		if err != nil {
			return "", 0, err
		}
		return fresh.AccessToken, generation, nil
	}
	return s.AccessToken, generation, nil
}

// replayToken returns the access token to send a request with again after
// the API refused its token sent with a 401; generation is what bearerFor
// returned with sent. It reports false when the request must not be sent
// again: the user signed out or another session was set since, so a replay
// would run without a session or as another user, or the session has no
// refresh token. A session that a refresh installed in the meantime is used
// as it is; otherwise the session is refreshed first.
func (a *AuthClient) replayToken(ctx context.Context, sent string, generation uint64) (string, bool, error) {
	a.mu.Lock()
	s, same := a.session, a.generation == generation
	a.mu.Unlock()
	if !same || s == nil || s.RefreshToken == "" {
		return "", false, nil
	}
	if s.AccessToken != sent {
		return s.AccessToken, true, nil
	}
	if _, err := a.RefreshSession(ctx); err != nil {
		return "", false, err
	}
	// The refresh may have found the session replaced or cleared; read the
	// token and the generation under one lock.
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.generation != generation || a.session == nil || a.session.AccessToken == sent {
		return "", false, nil
	}
	return a.session.AccessToken, true, nil
}

// MarshalSession serializes a session for storage (include RefreshToken).
func MarshalSession(s *Session) ([]byte, error) { return json.Marshal(s) }

// UnmarshalSession restores a session produced by MarshalSession.
func UnmarshalSession(data []byte) (*Session, error) {
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
