package vaif

import (
	"encoding/json"
	"net/http"
	"time"
)

// Row is one table row as decoded from JSON.
type Row = map[string]any

// DecodeRow converts a Row into a struct (via its json tags).
func DecodeRow(row Row, dest any) error {
	raw, err := json.Marshal(row)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dest)
}

// User is a project end user.
type User struct {
	ID              string         `json:"id"`
	Email           *string        `json:"email,omitempty"`
	Phone           *string        `json:"phone,omitempty"`
	Provider        *string        `json:"provider,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	EmailVerifiedAt *string        `json:"emailVerifiedAt,omitempty"`
	CreatedAt       *string        `json:"createdAt,omitempty"`
}

// Session is an authenticated end-user session.
//
// RefreshToken is the opaque, rotating token the API delivers in the
// project_refresh_token cookie. Persist it securely to survive restarts.
type Session struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	ExpiresAt    time.Time `json:"expiresAt"`
	User         *User     `json:"user,omitempty"`
}

// ExpiresWithin reports whether the access token expires within d.
func (s *Session) ExpiresWithin(d time.Duration) bool {
	return time.Until(s.ExpiresAt) <= d
}

// SignInResult is either a Session or an MFA challenge.
type SignInResult struct {
	Session        *Session
	MFARequired    bool
	ChallengeToken string
}

// SignUpResult is the outcome of SignUp. Session is nil when the API answers
// "pending verification" (it does not reveal whether an address exists).
type SignUpResult struct {
	Session             *Session
	User                *User
	PendingVerification bool
	Message             string
}

// ListResult holds rows and the total number of matching rows.
type ListResult struct {
	Data  []Row
	Count int
}

// Page is one page of offset pagination (Page starts at 1).
type Page struct {
	Data     []Row
	Total    int
	Page     int
	PageSize int
}

// PageCount is the number of pages.
func (p *Page) PageCount() int {
	if p.PageSize <= 0 {
		return 0
	}
	return (p.Total + p.PageSize - 1) / p.PageSize
}

// HasNext reports whether a later page exists.
func (p *Page) HasNext() bool { return p.Page < p.PageCount() }

// HasPrevious reports whether an earlier page exists.
func (p *Page) HasPrevious() bool { return p.Page > 1 }

// UploadResult describes a stored object.
type UploadResult struct {
	Key    string
	Bucket string
	Size   int
}

// FunctionResponse is the HTTP response a function produced.
type FunctionResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

// JSON decodes the body into v.
func (r *FunctionResponse) JSON(v any) error { return json.Unmarshal(r.Body, v) }

// Text returns the body as a string.
func (r *FunctionResponse) Text() string { return string(r.Body) }
