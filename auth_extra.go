package vaif

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"regexp"
)

// Where a VAIF Studio account is created and its password reset.
const (
	ConsoleSignUpURL        = "https://console.vaif.studio/register"
	ConsolePasswordResetURL = "https://console.vaif.studio/forgot-password"
)

// ConsoleOnlyDetails is the Details of a CONSOLE_ONLY error: the call that
// was refused and the console page that does it instead.
type ConsoleOnlyDetails struct {
	// Method is the SDK call that was refused.
	Method string
	// ConsoleURL is the console page to send the person to.
	ConsoleURL string
}

// IsConsoleOnly reports whether err is the CONSOLE_ONLY refusal of a platform
// sign-up or password reset, and returns its details.
func IsConsoleOnly(err error) (*ConsoleOnlyDetails, bool) {
	var e *Error
	if !errors.As(err, &e) || e.Code != "CONSOLE_ONLY" {
		return nil, false
	}
	d, ok := e.Details.(*ConsoleOnlyDetails)
	return d, ok
}

// consoleOnlySignUp is the error a platform client's Auth.SignUp and the
// generated PostAuthSignup return before any request: the console's sign-up
// page carries a bot check an SDK cannot pass.
func consoleOnlySignUp(method, ownApp string) *Error {
	return &Error{
		Kind:    ErrConsoleOnly,
		Code:    "CONSOLE_ONLY",
		Message: "Create your VAIF Studio account in the console at " + ConsoleSignUpURL + ". To sign up users of your own app, " + ownApp + ".",
		Details: &ConsoleOnlyDetails{Method: method, ConsoleURL: ConsoleSignUpURL},
	}
}

// consoleOnlyPasswordReset is the password-reset counterpart.
func consoleOnlyPasswordReset(method, ownApp string) *Error {
	return &Error{
		Kind:    ErrConsoleOnly,
		Code:    "CONSOLE_ONLY",
		Message: "Reset your VAIF Studio password in the console at " + ConsolePasswordResetURL + ". To reset passwords for users of your own app, " + ownApp + ".",
		Details: &ConsoleOnlyDetails{Method: method, ConsoleURL: ConsolePasswordResetURL},
	}
}

const ownAppClient = "create the client with NewClient and your projectID"

// MFAEnrollment starts TOTP enrollment (POST …/auth/mfa/enroll-start).
type MFAEnrollment struct {
	EnrollToken      string `json:"enrollToken"`
	OTPAuthURI       string `json:"otpauthUri"`
	Secret           string `json:"secret"`
	ExpiresInSeconds int    `json:"expiresInSeconds"`
}

// MFABackupCodes are one-time recovery codes. Show them once; never log them.
type MFABackupCodes struct {
	OK          bool     `json:"ok"`
	BackupCodes []string `json:"backupCodes"`
}

func (a *AuthClient) mfaCall(ctx context.Context, suffix string, body, out any) error {
	resp, err := a.client.send(ctx, request{method: http.MethodPost, path: a.path("mfa/" + suffix), body: body}, true)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return decodeJSON(resp, out)
}

// EnrollMFA begins TOTP enrollment. Display the secret once; do not log it.
func (a *AuthClient) EnrollMFA(ctx context.Context) (*MFAEnrollment, error) {
	var out MFAEnrollment
	err := a.mfaCall(ctx, "enroll-start", nil, &out)
	return &out, err
}

// ConfirmMFA finishes enrollment with the enrollment token and a TOTP code.
func (a *AuthClient) ConfirmMFA(ctx context.Context, token, code string) (*MFABackupCodes, error) {
	var out MFABackupCodes
	err := a.mfaCall(ctx, "enroll-confirm", map[string]any{"enrollToken": token, "code": code}, &out)
	return &out, err
}

// DisableMFA turns MFA off for the signed-in user (needs a current code).
func (a *AuthClient) DisableMFA(ctx context.Context, code string) error {
	return a.mfaCall(ctx, "disable", map[string]any{"code": code}, nil)
}

// RegenerateMFACodes replaces the backup codes (needs a current code).
func (a *AuthClient) RegenerateMFACodes(ctx context.Context, code string) (*MFABackupCodes, error) {
	var out MFABackupCodes
	err := a.mfaCall(ctx, "regenerate-codes", map[string]any{"code": code}, &out)
	return &out, err
}

// OAuthOptions starts app-user social login. Open the returned URL yourself.
type OAuthOptions struct{ Provider, RedirectURL, State, CodeVerifier string }

// OAuthSignIn is the provider URL to open plus the State and PKCE
// CodeVerifier to keep for the callback.
type OAuthSignIn struct {
	URL                 string `json:"url"`
	ExpiresIn           int    `json:"expiresIn"`
	State, CodeVerifier string
}

var verifierRE = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

func randomURLToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// GetOAuthSignInURL starts app-user social sign-in with an S256 PKCE
// challenge. State and CodeVerifier are generated when empty.
func (a *AuthClient) GetOAuthSignInURL(ctx context.Context, opts OAuthOptions) (*OAuthSignIn, error) {
	if a.client.projectID == "" {
		return nil, &Error{Kind: ErrInvalidArg, Code: "PROJECT_ID_REQUIRED", Message: "OAuth requires a projectID"}
	}
	if opts.Provider != "google" && opts.Provider != "github" {
		return nil, invalidArg("provider must be google or github")
	}
	redirect, err := url.Parse(opts.RedirectURL)
	if err != nil || redirect.Host == "" || (redirect.Scheme != "https" && redirect.Scheme != "http") {
		return nil, invalidArg("redirectURL must be an absolute http(s) URL")
	}
	if opts.CodeVerifier == "" {
		opts.CodeVerifier, err = randomURLToken()
		if err != nil {
			return nil, err
		}
	}
	if !verifierRE.MatchString(opts.CodeVerifier) {
		return nil, invalidArg("codeVerifier must be 43-128 PKCE characters")
	}
	if opts.State == "" {
		opts.State, err = randomURLToken()
		if err != nil {
			return nil, err
		}
	}
	digest := sha256.Sum256([]byte(opts.CodeVerifier))
	resp, err := a.client.do(ctx, request{method: http.MethodPost, path: a.path("oauth/" + opts.Provider + "/start"), body: map[string]any{"redirectUrl": opts.RedirectURL, "state": opts.State, "codeChallenge": base64.RawURLEncoding.EncodeToString(digest[:]), "codeChallengeMethod": "S256"}})
	if err != nil {
		return nil, err
	}
	var out OAuthSignIn
	if err = decodeJSON(resp, &out); err != nil {
		return nil, err
	}
	if out.URL == "" {
		return nil, &Error{Kind: ErrBadResponse, Message: "OAuth response has no URL"}
	}
	out.State, out.CodeVerifier = opts.State, opts.CodeVerifier
	return &out, nil
}

// ExchangeOAuthCode consumes a one-time code. The caller must validate the
// callback state against GetOAuthSignInURL's State before calling this method.
func (a *AuthClient) ExchangeOAuthCode(ctx context.Context, code, verifier string) (*SignInResult, error) {
	if code == "" || !verifierRE.MatchString(verifier) {
		return nil, invalidArg("code and valid PKCE verifier are required")
	}
	resp, err := a.client.do(ctx, request{method: http.MethodPost, path: a.path("oauth/token"), body: map[string]any{"code": code, "codeVerifier": verifier}})
	if err != nil {
		return nil, err
	}
	var body tokenBody
	if err = decodeJSON(resp, &body); err != nil {
		return nil, err
	}
	if body.MFARequired {
		return &SignInResult{MFARequired: true, ChallengeToken: body.ChallengeToken}, nil
	}
	session, err := sessionFrom(&body, refreshTokenFrom(resp.header), resp.status)
	if err != nil {
		return nil, err
	}
	a.set(session, EventSignedIn)
	return &SignInResult{Session: session}, nil
}
