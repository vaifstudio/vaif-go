package vaif

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// RealtimeClient opens independent database-change subscriptions. Credentials
// travel in the first frame, never the URL. Resubscribe after Next returns an
// error; reconnection, presence and broadcasts are not automatic.
type RealtimeClient struct {
	client *Client
	dial   func(context.Context, string) (realtimeSocket, error)
}
type realtimeSocket interface {
	WriteJSON(any) error
	ReadJSON(any) error
	SetReadDeadline(time.Time) error
	Close() error
}

// RealtimeEvent is a server frame; Next returns only db_change events.
type RealtimeEvent struct {
	Type                string   `json:"type"`
	Channel             string   `json:"channel,omitempty"`
	Channels            []string `json:"channels,omitempty"`
	Table               string   `json:"table,omitempty"`
	Op                  string   `json:"op,omitempty"`
	Record              Row      `json:"record,omitempty"`
	OldRecord           Row      `json:"old_record,omitempty"`
	Code                string   `json:"code,omitempty"`
	Message             string   `json:"message,omitempty"`
	Error               string   `json:"error,omitempty"`
	HeartbeatIntervalMS int      `json:"heartbeatIntervalMs,omitempty"`
}

// RealtimeFilter narrows a subscription on the server to the rows where
// Field Operator Value holds. Operator is one of eq, neq, gt, lt, gte, lte,
// in, like or ilike (default eq). For "in", Value may be a []string.
type RealtimeFilter struct {
	Field    string
	Operator string
	Value    any
}

// SubscribeOptions narrows a subscription: Filters select rows on the server
// and Columns restricts the delivered columns.
type SubscribeOptions struct {
	Filters []RealtimeFilter
	Columns []string
}

var realtimeOperators = map[string]bool{"eq": true, "neq": true, "gt": true, "lt": true, "gte": true, "lte": true, "in": true, "like": true, "ilike": true}

// encodeURIComponent escapes as JavaScript's encodeURIComponent does for the
// characters that matter here (a space is %20, never +).
func encodeURIComponent(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// tableChannel builds `table:<table>[?<field>.<op>=<value>&…&columns=a,b]`,
// the channel format the API's realtime endpoint reads.
func tableChannel(table string, opts *SubscribeOptions) (string, error) {
	if !isIdentifier(table) {
		return "", invalidArg("table must be an identifier")
	}
	if opts == nil {
		return "table:" + table, nil
	}
	var parts []string
	for _, f := range opts.Filters {
		if !isIdentifier(f.Field) {
			return "", invalidArg("filter field must be an identifier, got %q", f.Field)
		}
		op := f.Operator
		if op == "" {
			op = "eq"
		}
		if !realtimeOperators[op] {
			return "", invalidArg("filter operator %q is not one of eq, neq, gt, lt, gte, lte, in, like, ilike", op)
		}
		var value string
		switch v := f.Value.(type) {
		case []string:
			value = strings.Join(v, ",")
		case nil:
			return "", invalidArg("filter %s needs a value", f.Field)
		default:
			value = fmt.Sprint(v)
		}
		parts = append(parts, encodeURIComponent(f.Field)+"."+op+"="+encodeURIComponent(value))
	}
	if len(opts.Columns) > 0 {
		for _, col := range opts.Columns {
			if !isIdentifier(col) {
				return "", invalidArg("column must be an identifier")
			}
		}
		parts = append(parts, "columns="+strings.Join(opts.Columns, ","))
	}
	if len(parts) == 0 {
		return "table:" + table, nil
	}
	return "table:" + table + "?" + strings.Join(parts, "&"), nil
}

// Subscription is one open table subscription. Close it when done.
type Subscription struct {
	socket realtimeSocket
	// idle bounds each read in Next: the client pings every heartbeat and the
	// server answers each ping, so a healthy socket is never silent this long.
	idle   time.Duration
	ctx    context.Context
	cancel context.CancelFunc
	writes sync.Mutex
	once   sync.Once
	done   chan struct{}
}

// realtimeURL is the socket URL for baseURL: https becomes wss, and http
// becomes ws only for the hosts allowsCleartext names. Anything else is
// refused with INVALID_REALTIME_URL, so the first frame (API key and access
// token) never travels unencrypted. Subscribe calls it before it reads a
// token or dials.
func realtimeURL(baseURL string) (string, error) {
	refused := func() error {
		return &Error{Kind: ErrInvalidArg, Code: "INVALID_REALTIME_URL",
			Message: "realtime needs an https base URL (http only for localhost, 127.0.0.1 or ::1)"}
	}
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return "", refused()
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		u.Scheme = "wss"
	case "http":
		if !allowsCleartext(u.Hostname()) {
			return "", refused()
		}
		u.Scheme = "ws"
	default:
		return "", refused()
	}
	return strings.TrimRight(u.String(), "/") + "/realtime/ws", nil
}

func newRealtimeClient(c *Client) *RealtimeClient {
	r := &RealtimeClient{client: c}
	r.dial = func(ctx context.Context, target string) (realtimeSocket, error) {
		d := websocket.Dialer{HandshakeTimeout: c.timeout, Proxy: http.ProxyFromEnvironment}
		conn, resp, err := d.DialContext(ctx, target, nil)
		if err != nil {
			if resp != nil && resp.Body != nil {
				resp.Body.Close()
			}
			return nil, err
		}
		conn.SetReadLimit(maxResponseBytes)
		return conn, nil
	}
	return r
}
func (s *Subscription) write(v any) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	return s.socket.WriteJSON(v)
}

// Close ends the subscription and its socket. It is safe to call twice.
func (s *Subscription) Close() error {
	var err error
	s.once.Do(func() { s.cancel(); err = s.socket.Close(); close(s.done) })
	return err
}

// realtimeError wraps a socket error as an *Error: ErrTimeout when a read
// deadline passed (the socket was silent for idle), ErrNetwork otherwise. The
// socket's own error, such as io.EOF or a *websocket.CloseError, stays
// reachable through errors.Is and errors.As.
func realtimeError(err error, idle time.Duration) *Error {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &Error{Kind: ErrTimeout, Message: fmt.Sprintf("realtime connection was silent for %s", idle), Err: err}
	}
	return &Error{Kind: ErrNetwork, Message: "realtime connection closed", Err: err}
}

func realtimeFrameError(e RealtimeEvent) error {
	code := e.Code
	if code == "" {
		code = e.Error
	}
	return &Error{Kind: ErrRequest, Code: code, Message: e.Message}
}

// Subscribe waits for the server's subscription acknowledgment. The context
// owns the connection lifetime; call Close when finished. Next has one reader.
func (r *RealtimeClient) Subscribe(ctx context.Context, table string, opts *SubscribeOptions) (*Subscription, error) {
	channel, err := tableChannel(table, opts)
	if err != nil {
		return nil, err
	}
	target, err := realtimeURL(r.client.baseURL)
	if err != nil {
		return nil, err
	}
	token, err := r.client.Auth.bearer(ctx)
	if err != nil {
		return nil, err
	}
	socket, err := r.dial(ctx, target)
	if err != nil {
		return nil, networkError(err, r.client.timeout)
	}
	life, cancel := context.WithCancel(ctx)
	s := &Subscription{socket: socket, ctx: life, cancel: cancel, done: make(chan struct{})}
	go func() {
		select {
		case <-life.Done():
			s.Close()
		case <-s.done:
		}
	}()
	// Bound authentication/acknowledgment even when the caller has no deadline.
	var timedOut atomic.Bool
	timer := time.AfterFunc(r.client.timeout, func() { timedOut.Store(true); s.Close() })
	defer timer.Stop()
	fail := func(e error) (*Subscription, error) { s.Close(); return nil, e }
	// broken is the error for a failed write or read: the caller's context
	// ended, the acknowledgment timer closed the socket, or the socket failed.
	broken := func(err error) (*Subscription, error) {
		switch {
		case ctx.Err() != nil:
			return fail(canceled(ctx.Err()))
		case timedOut.Load():
			return fail(&Error{Kind: ErrTimeout, Message: fmt.Sprintf("realtime subscription was not acknowledged within %s", r.client.timeout), Err: err})
		default:
			return fail(realtimeError(err, r.client.timeout))
		}
	}
	auth := map[string]any{"type": "auth", "apiKey": r.client.apiKey}
	if token != "" {
		auth["accessToken"] = token
	}
	if err = s.write(auth); err != nil {
		return broken(err)
	}
	var welcome RealtimeEvent
	if err = socket.ReadJSON(&welcome); err != nil {
		return broken(err)
	}
	if welcome.Type == "error" {
		return fail(realtimeFrameError(welcome))
	}
	if welcome.Type != "welcome" {
		return fail(&Error{Kind: ErrBadResponse, Message: "expected realtime welcome"})
	}
	if err = s.write(map[string]string{"type": "subscribe", "channel": channel}); err != nil {
		return broken(err)
	}
	for {
		var ack RealtimeEvent
		if err = socket.ReadJSON(&ack); err != nil {
			return broken(err)
		}
		if ack.Type == "error" {
			return fail(realtimeFrameError(ack))
		}
		if ack.Type == "subscribed" && ack.Channel == channel {
			break
		}
	}
	interval := 25 * time.Second
	if welcome.HeartbeatIntervalMS > 0 {
		interval = time.Duration(welcome.HeartbeatIntervalMS) * time.Millisecond / 2
	}
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	s.idle = 3 * interval
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-life.Done():
				return
			case <-ticker.C:
				if s.write(map[string]string{"type": "ping"}) != nil {
					s.Close()
					return
				}
			}
		}
	}()
	return s, nil
}

// Next waits for the next database-change event. Canceling ctx closes the
// subscription to interrupt a blocked read, and a socket silent for three
// heartbeats (no pong, no frame) is closed as dead. Every error is an *Error:
// a closed or failed socket matches ErrNetwork, a silent one ErrTimeout, and
// a canceled ctx still matches context.Canceled. Callers must serialize Next
// calls; after an error, Subscribe again.
func (s *Subscription) Next(ctx context.Context) (*RealtimeEvent, error) {
	stop := context.AfterFunc(ctx, func() { s.Close() })
	defer stop()
	for {
		var event RealtimeEvent
		if s.idle > 0 {
			_ = s.socket.SetReadDeadline(time.Now().Add(s.idle))
		}
		if err := s.socket.ReadJSON(&event); err != nil {
			s.Close()
			if ctx.Err() != nil {
				return nil, canceled(ctx.Err())
			}
			return nil, realtimeError(err, s.idle)
		}
		if event.Type == "error" {
			s.Close()
			return nil, realtimeFrameError(event)
		}
		if event.Type == "db_change" {
			return &event, nil
		}
	}
}
