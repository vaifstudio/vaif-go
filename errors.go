package vaif

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Sentinel errors for errors.Is. Every error returned by this package that
// comes from the API or the network is an *Error, which matches exactly one of
// these (ErrTimeout also matches ErrNetwork).
var (
	ErrValidation  = errors.New("vaif: validation failed")   // 400, 422
	ErrAuth        = errors.New("vaif: not authorized")      // 401, 403
	ErrNotFound    = errors.New("vaif: not found")           // 404
	ErrConflict    = errors.New("vaif: conflict")            // 409
	ErrRateLimited = errors.New("vaif: rate limited")        // 429
	ErrServer      = errors.New("vaif: server error")        // 5xx
	ErrRequest     = errors.New("vaif: request failed")      // any other non-2xx
	ErrNetwork     = errors.New("vaif: network error")       // no usable response
	ErrTimeout     = errors.New("vaif: request timed out")   // deadline hit
	ErrInvalidArg  = errors.New("vaif: invalid argument")    // rejected before sending
	ErrBadResponse = errors.New("vaif: unexpected response") // 2xx we could not decode
	// ErrConsoleOnly: a platform sign-up or password reset, refused before
	// sending (code CONSOLE_ONLY). Use the console; see IsConsoleOnly.
	ErrConsoleOnly = errors.New("vaif: console only")
)

// Error describes a failed API call. Use errors.As to read the details:
//
//	var apiErr *vaif.Error
//	if errors.As(err, &apiErr) && apiErr.Code == "INVALID_COLUMN" { ... }
type Error struct {
	// Kind is one of the sentinel errors above.
	Kind error
	// Status is the HTTP status, or 0 when no response was received.
	Status int
	// Code is the server's machine-readable code (e.g. "INVALID_CREDENTIALS").
	Code string
	// Message is a human-readable description.
	Message string
	// Details carries structured details from the server, when present.
	Details any
	// RequestID identifies the request in server logs.
	RequestID string
	// RetryAfter is the server's Retry-After hint on 429 responses.
	RetryAfter time.Duration
	// Body is the decoded response body (JSON value, or string).
	Body any
	// Err is the underlying cause for network errors.
	Err error

	// header is the response's header, nil when no response was received.
	header http.Header
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("vaif: ")
	b.WriteString(e.Message)
	if e.Status != 0 {
		fmt.Fprintf(&b, " (status %d", e.Status)
		if e.Code != "" {
			fmt.Fprintf(&b, ", code %s", e.Code)
		}
		b.WriteString(")")
	}
	return b.String()
}

// Unwrap exposes the network cause, so errors.Is(err, context.Canceled) works.
func (e *Error) Unwrap() error { return e.Err }

// Is matches the sentinel kind (ErrTimeout also matches ErrNetwork).
func (e *Error) Is(target error) bool {
	if target == e.Kind {
		return true
	}
	return e.Kind == ErrTimeout && target == ErrNetwork
}

func invalidArg(format string, args ...any) error {
	return &Error{Kind: ErrInvalidArg, Message: fmt.Sprintf(format, args...)}
}

func kindForStatus(status int) error {
	switch {
	case status == 400 || status == 422:
		return ErrValidation
	case status == 401 || status == 403:
		return ErrAuth
	case status == 404:
		return ErrNotFound
	case status == 409:
		return ErrConflict
	case status == 429:
		return ErrRateLimited
	case status >= 500:
		return ErrServer
	default:
		return ErrRequest
	}
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(value, 64); err == nil && secs > 0 {
		return time.Duration(secs * float64(time.Second))
	}
	if when, err := http.ParseTime(value); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}

// errorFromResponse maps a non-2xx response to *Error.
func errorFromResponse(status int, header http.Header, raw []byte, reason string) *Error {
	e := &Error{Kind: kindForStatus(status), Status: status, header: header}
	var body any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			text := string(raw)
			if len(text) > 500 {
				text = text[:500]
			}
			body = text
		}
	}
	e.Body = body
	if m, ok := body.(map[string]any); ok {
		msg, _ := m["message"].(string)
		errField, _ := m["error"].(string)
		code, _ := m["code"].(string)
		// The copilot envelope nests them: {"error": {"code": ..., "message": ...}}.
		if nested, ok := m["error"].(map[string]any); ok {
			if code == "" {
				code, _ = nested["code"].(string)
			}
			if msg == "" {
				msg, _ = nested["message"].(string)
			}
		}
		if msg == "" {
			msg = errField
		}
		if code == "" {
			code = errField
		}
		e.Message, e.Code = msg, code
		e.Details = m["details"]
		e.RequestID, _ = m["requestId"].(string)
	} else if s, ok := body.(string); ok && s != "" {
		e.Message = s
	}
	if e.Message == "" {
		e.Message = reason
	}
	if e.Message == "" {
		e.Message = fmt.Sprintf("HTTP %d", status)
	}
	if e.RequestID == "" {
		e.RequestID = header.Get("X-Request-Id")
	}
	if status == 429 {
		e.RetryAfter = parseRetryAfter(header.Get("Retry-After"))
	}
	return e
}
