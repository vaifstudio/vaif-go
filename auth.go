package vaif

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// AuthStateChangeCallback is called when auth state changes.
type AuthStateChangeCallback func(event AuthChangeEvent, session *Session)

// authCallback wraps a callback with a unique ID for safe unsubscription.
type authCallback struct {
	id uint64
	fn AuthStateChangeCallback
}

// Auth provides authentication and user management. All methods are safe
// for concurrent use.
type Auth struct {
	client         *Client
	currentSession *Session
	currentUser    *User
	callbacks      []authCallback
	nextCallbackID uint64
	mu             sync.RWMutex

	// Token auto-refresh
	refreshCancel context.CancelFunc
	refreshDone   chan struct{}
}

func newAuth(c *Client) *Auth {
	return &Auth{
		client:    c,
		callbacks: make([]authCallback, 0),
	}
}

// GetSession returns the current session, or nil if not authenticated.
func (a *Auth) GetSession() *Session {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.currentSession
}

// GetUser fetches the current user from the API. Returns nil if no session
// is active.
func (a *Auth) GetUser(ctx context.Context) (*User, error) {
	a.mu.RLock()
	session := a.currentSession
	a.mu.RUnlock()

	if session == nil {
		return nil, nil
	}

	raw, err := a.client.request(ctx, "GET", "/auth/user", nil, nil)
	if err != nil {
		return nil, err
	}

	resp := parseResponse[User](raw)
	if resp.Error != nil {
		return nil, &AuthError{Message: resp.Error.Message, Status: resp.Status, sentinel: resp.Error.sentinel}
	}

	a.mu.Lock()
	a.currentUser = &resp.Data
	a.mu.Unlock()

	return &resp.Data, nil
}

// SignUp creates a new user with email and password.
func (a *Auth) SignUp(ctx context.Context, opts SignUpOptions) (*AuthResponse, error) {
	body := map[string]any{}
	if opts.Email != "" {
		body["email"] = opts.Email
	}
	if opts.Password != "" {
		body["password"] = opts.Password
	}
	if opts.Phone != "" {
		body["phone"] = opts.Phone
	}
	if opts.Data != nil {
		body["data"] = opts.Data
	}
	if opts.RedirectTo != "" {
		body["redirectTo"] = opts.RedirectTo
	}

	raw, err := a.client.request(ctx, "POST", "/auth/signup", body, nil)
	if err != nil {
		return nil, err
	}

	return a.handleAuthResponse(raw)
}

// SignUpOptions configures sign-up.
type SignUpOptions struct {
	Email      string
	Password   string
	Phone      string
	Data       map[string]any
	RedirectTo string
}

// SignInWithPassword signs in with email/phone and password.
func (a *Auth) SignInWithPassword(ctx context.Context, opts SignInOptions) (*AuthResponse, error) {
	body := map[string]any{}
	if opts.Email != "" {
		body["email"] = opts.Email
	}
	if opts.Password != "" {
		body["password"] = opts.Password
	}
	if opts.Phone != "" {
		body["phone"] = opts.Phone
	}

	raw, err := a.client.request(ctx, "POST", "/auth/token?grant_type=password", body, nil)
	if err != nil {
		return nil, err
	}

	return a.handleAuthResponse(raw)
}

// SignInOptions configures sign-in.
type SignInOptions struct {
	Email    string
	Password string
	Phone    string
}

// SignInWithOAuth returns the URL to initiate an OAuth flow.
func (a *Auth) SignInWithOAuth(provider OAuthProvider, redirectTo string, scopes []string) (string, error) {
	q := url.Values{}
	q.Set("provider", string(provider))
	if redirectTo != "" {
		q.Set("redirect_to", redirectTo)
	}
	if len(scopes) > 0 {
		q.Set("scopes", strings.Join(scopes, " "))
	}

	return fmt.Sprintf("%s/auth/authorize?%s", a.client.config.APIURL, q.Encode()), nil
}

// SignInWithOTP sends a magic link or OTP to the given email or phone.
func (a *Auth) SignInWithOTP(ctx context.Context, opts OTPOptions) error {
	body := map[string]any{"create_user": opts.CreateUser}
	if opts.Email != "" {
		body["email"] = opts.Email
	}
	if opts.Phone != "" {
		body["phone"] = opts.Phone
	}
	if opts.RedirectTo != "" {
		body["redirect_to"] = opts.RedirectTo
	}

	raw, err := a.client.request(ctx, "POST", "/auth/otp", body, nil)
	if err != nil {
		return err
	}

	resp := parseResponse[any](raw)
	if resp.Error != nil {
		return &AuthError{Message: resp.Error.Message, Status: resp.Status, sentinel: resp.Error.sentinel}
	}

	return nil
}

// OTPOptions configures OTP sign-in.
type OTPOptions struct {
	Email      string
	Phone      string
	CreateUser bool
	RedirectTo string
}

// VerifyOTP verifies an OTP code and returns a session.
func (a *Auth) VerifyOTP(ctx context.Context, opts VerifyOTPOptions) (*AuthResponse, error) {
	body := map[string]any{
		"token": opts.Token,
		"type":  opts.Type,
	}
	if opts.Email != "" {
		body["email"] = opts.Email
	}
	if opts.Phone != "" {
		body["phone"] = opts.Phone
	}

	raw, err := a.client.request(ctx, "POST", "/auth/verify", body, nil)
	if err != nil {
		return nil, err
	}

	return a.handleAuthResponse(raw)
}

// VerifyOTPOptions configures OTP verification.
type VerifyOTPOptions struct {
	Token string
	Type  string // "sms", "email", "magiclink"
	Email string
	Phone string
}

// SignOut signs out the current user. Scope can be "local" (default),
// "global" (all sessions), or "others" (all except current).
func (a *Auth) SignOut(ctx context.Context, scope string) error {
	if scope == "" {
		scope = "local"
	}

	if scope != "local" {
		raw, err := a.client.request(ctx, "POST", "/auth/logout", map[string]any{"scope": scope}, nil)
		if err != nil {
			return err
		}
		resp := parseResponse[any](raw)
		if resp.Error != nil {
			return &AuthError{Message: resp.Error.Message, Status: resp.Status, sentinel: resp.Error.sentinel}
		}
	}

	a.stopAutoRefresh()
	a.clearSession()
	a.notifyStateChange(AuthEventSignedOut, nil)
	return nil
}

// ResetPasswordForEmail sends a password reset email.
func (a *Auth) ResetPasswordForEmail(ctx context.Context, email, redirectTo string) error {
	body := map[string]any{"email": email}
	if redirectTo != "" {
		body["redirect_to"] = redirectTo
	}

	raw, err := a.client.request(ctx, "POST", "/auth/recover", body, nil)
	if err != nil {
		return err
	}

	resp := parseResponse[any](raw)
	if resp.Error != nil {
		return &AuthError{Message: resp.Error.Message, Status: resp.Status, sentinel: resp.Error.sentinel}
	}

	return nil
}

// UpdateUser updates the current user's profile.
func (a *Auth) UpdateUser(ctx context.Context, opts UpdateUserOptions) (*User, error) {
	body := map[string]any{}
	if opts.Email != "" {
		body["email"] = opts.Email
	}
	if opts.Password != "" {
		body["password"] = opts.Password
	}
	if opts.Phone != "" {
		body["phone"] = opts.Phone
	}
	if opts.Data != nil {
		body["data"] = opts.Data
	}

	raw, err := a.client.request(ctx, "PUT", "/auth/user", body, nil)
	if err != nil {
		return nil, err
	}

	resp := parseResponse[User](raw)
	if resp.Error != nil {
		return nil, &AuthError{Message: resp.Error.Message, Status: resp.Status, sentinel: resp.Error.sentinel}
	}

	a.mu.Lock()
	a.currentUser = &resp.Data
	a.mu.Unlock()

	a.notifyStateChange(AuthEventUserUpdated, a.GetSession())
	return &resp.Data, nil
}

// UpdateUserOptions configures user updates.
type UpdateUserOptions struct {
	Email    string
	Password string
	Phone    string
	Data     map[string]any
}

// RefreshSession refreshes the current session using the refresh token.
func (a *Auth) RefreshSession(ctx context.Context) (*AuthResponse, error) {
	a.mu.RLock()
	session := a.currentSession
	a.mu.RUnlock()

	if session == nil || session.RefreshToken == "" {
		return &AuthResponse{
			Error: &AuthError{Message: "No refresh token available", Status: 401, sentinel: ErrUnauthorized},
		}, nil
	}

	body := map[string]any{"refresh_token": session.RefreshToken}
	raw, err := a.client.request(ctx, "POST", "/auth/token?grant_type=refresh_token", body, nil)
	if err != nil {
		return nil, err
	}

	resp := a.parseSessionResponse(raw)
	if resp.Error != nil {
		a.clearSession()
		a.notifyStateChange(AuthEventTokenRefreshed, nil)
		return resp, nil
	}

	if resp.Session != nil {
		a.setSession(resp.Session)
		a.notifyStateChange(AuthEventTokenRefreshed, resp.Session)
	}

	return resp, nil
}

// ExchangeCodeForSession exchanges an authorization code for a session (PKCE flow).
func (a *Auth) ExchangeCodeForSession(ctx context.Context, code string) (*AuthResponse, error) {
	body := map[string]any{"code": code}
	raw, err := a.client.request(ctx, "POST", "/auth/token?grant_type=authorization_code", body, nil)
	if err != nil {
		return nil, err
	}

	return a.handleAuthResponse(raw)
}

// OnAuthStateChange registers a callback for auth state changes.
// Returns an unsubscribe function that removes the callback.
func (a *Auth) OnAuthStateChange(callback AuthStateChangeCallback) func() {
	a.mu.Lock()
	id := a.nextCallbackID
	a.nextCallbackID++
	a.callbacks = append(a.callbacks, authCallback{id: id, fn: callback})
	a.mu.Unlock()

	// Fire immediately with current state
	a.mu.RLock()
	session := a.currentSession
	a.mu.RUnlock()

	if session != nil {
		callback(AuthEventInitialSession, session)
	}

	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		for i, cb := range a.callbacks {
			if cb.id == id {
				a.callbacks = append(a.callbacks[:i], a.callbacks[i+1:]...)
				return
			}
		}
	}
}

func (a *Auth) handleAuthResponse(raw *apiRawResponse) (*AuthResponse, error) {
	resp := a.parseSessionResponse(raw)
	if resp.Error != nil {
		return resp, nil
	}

	if resp.Session != nil {
		a.setSession(resp.Session)
		a.notifyStateChange(AuthEventSignedIn, resp.Session)

		// Start auto-refresh if enabled
		if a.client.config.AutoRefreshToken {
			a.startAutoRefresh(resp.Session)
		}
	}

	return resp, nil
}

func (a *Auth) parseSessionResponse(raw *apiRawResponse) *AuthResponse {
	resp := parseResponse[map[string]any](raw)

	if resp.Error != nil {
		return &AuthResponse{
			Error: &AuthError{Message: resp.Error.Message, Status: resp.Status, sentinel: resp.Error.sentinel},
		}
	}

	result := &AuthResponse{}

	if sessionData, ok := resp.Data["session"].(map[string]any); ok {
		session := &Session{}
		if v, ok := sessionData["access_token"].(string); ok {
			session.AccessToken = v
		}
		if v, ok := sessionData["refresh_token"].(string); ok {
			session.RefreshToken = v
		}
		if v, ok := sessionData["expires_in"].(float64); ok {
			session.ExpiresIn = int(v)
		}
		if v, ok := sessionData["expires_at"].(float64); ok {
			session.ExpiresAt = int(v)
		}
		if v, ok := sessionData["token_type"].(string); ok {
			session.TokenType = v
		}
		result.Session = session
	}

	if userData, ok := resp.Data["user"].(map[string]any); ok {
		user := parseUser(userData)
		result.User = user
		if result.Session != nil {
			result.Session.User = user
		}
	}

	return result
}

func parseUser(data map[string]any) *User {
	user := &User{}
	if v, ok := data["id"].(string); ok {
		user.ID = v
	}
	if v, ok := data["email"].(string); ok {
		user.Email = v
	}
	if v, ok := data["phone"].(string); ok {
		user.Phone = v
	}
	if v, ok := data["created_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			user.CreatedAt = t
		} else if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			user.CreatedAt = t
		}
	}
	if v, ok := data["updated_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			user.UpdatedAt = &t
		} else if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			user.UpdatedAt = &t
		}
	}
	if v, ok := data["email_confirmed_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			user.EmailConfirmedAt = &t
		} else if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			user.EmailConfirmedAt = &t
		}
	}
	if v, ok := data["phone_confirmed_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			user.PhoneConfirmedAt = &t
		} else if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			user.PhoneConfirmedAt = &t
		}
	}
	if v, ok := data["last_sign_in_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			user.LastSignInAt = &t
		} else if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			user.LastSignInAt = &t
		}
	}
	if v, ok := data["user_metadata"].(map[string]any); ok {
		user.UserMetadata = v
	}
	if v, ok := data["app_metadata"].(map[string]any); ok {
		user.AppMetadata = v
	}
	return user
}

// startAutoRefresh starts a background goroutine that refreshes the token
// before it expires.
func (a *Auth) startAutoRefresh(session *Session) {
	a.stopAutoRefresh()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	a.mu.Lock()
	a.refreshCancel = cancel
	a.refreshDone = done
	a.mu.Unlock()

	go func() {
		defer close(done)

		for {
			// Refresh at 80% of the token's lifetime to avoid expiry
			a.mu.RLock()
			s := a.currentSession
			a.mu.RUnlock()

			if s == nil || s.ExpiresIn <= 0 {
				return
			}

			refreshIn := time.Duration(float64(s.ExpiresIn)*0.8) * time.Second
			if refreshIn < 10*time.Second {
				refreshIn = 10 * time.Second
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(refreshIn):
			}

			resp, err := a.RefreshSession(ctx)
			if err != nil || (resp != nil && resp.Error != nil) {
				a.client.debug("auto-refresh failed: %v", err)
				return
			}
		}
	}()
}

// stopAutoRefresh cancels the auto-refresh goroutine if running.
func (a *Auth) stopAutoRefresh() {
	a.mu.RLock()
	cancel := a.refreshCancel
	done := a.refreshDone
	a.mu.RUnlock()

	if cancel != nil {
		cancel()
		if done != nil {
			<-done
		}

		a.mu.Lock()
		a.refreshCancel = nil
		a.refreshDone = nil
		a.mu.Unlock()
	}
}

func (a *Auth) setSession(session *Session) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.currentSession = session
	a.currentUser = session.User
	a.client.SetAccessToken(session.AccessToken)
}

func (a *Auth) clearSession() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.currentSession = nil
	a.currentUser = nil
	a.client.SetAccessToken("")
}

func (a *Auth) notifyStateChange(event AuthChangeEvent, session *Session) {
	a.mu.RLock()
	cbs := make([]authCallback, len(a.callbacks))
	copy(cbs, a.callbacks)
	a.mu.RUnlock()

	for _, cb := range cbs {
		func() {
			defer func() { recover() }()
			cb.fn(event, session)
		}()
	}
}
