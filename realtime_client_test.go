package vaif

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type fakeSocket struct {
	mu        sync.Mutex
	sent      []map[string]any
	frames    []RealtimeEvent
	end       error // returned once frames run out; io.EOF when nil
	closed    bool
	deadlines []time.Time
}

func (f *fakeSocket) SetReadDeadline(t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deadlines = append(f.deadlines, t)
	return nil
}

func (f *fakeSocket) WriteJSON(v any) error {
	b, _ := json.Marshal(v)
	var m map[string]any
	json.Unmarshal(b, &m)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return nil
}
func (f *fakeSocket) ReadJSON(v any) error {
	if len(f.frames) == 0 {
		if f.end != nil {
			return f.end
		}
		return io.EOF
	}
	b, _ := json.Marshal(f.frames[0])
	f.frames = f.frames[1:]
	return json.Unmarshal(b, v)
}
func (f *fakeSocket) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeSocket) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}
func TestRealtimeAuthSubscribeAndChange(t *testing.T) {
	c := parityClient(t, nil)
	f := &fakeSocket{frames: []RealtimeEvent{{Type: "welcome"}, {Type: "subscribed", Channel: "table:notes"}, {Type: "db_change", Table: "notes", Op: "INSERT", Record: Row{"id": "a"}}}}
	c.Realtime.dial = func(ctx context.Context, target string) (realtimeSocket, error) {
		if target != "wss://api.vaif.studio/realtime/ws" {
			t.Fatal(target)
		}
		return f, nil
	}
	sub, err := c.Realtime.Subscribe(context.Background(), "notes", nil)
	if err != nil {
		t.Fatal(err)
	}
	event, err := sub.Next(context.Background())
	if err != nil || event.Op != "INSERT" {
		t.Fatal(event, err)
	}
	if f.sent[0]["type"] != "auth" || f.sent[0]["apiKey"] != "vk_srv_test" || f.sent[1]["channel"] != "table:notes" {
		t.Fatal(f.sent)
	}
	f.mu.Lock()
	deadlines := len(f.deadlines)
	f.mu.Unlock()
	if deadlines == 0 {
		t.Fatal("Next read without a liveness deadline")
	}
	sub.Close()
	if !f.isClosed() {
		t.Fatal("socket not closed")
	}
}
func TestRealtimeRefusal(t *testing.T) {
	c := parityClient(t, nil)
	f := &fakeSocket{frames: []RealtimeEvent{{Type: "error", Code: "FORBIDDEN", Message: "denied"}}}
	c.Realtime.dial = func(context.Context, string) (realtimeSocket, error) { return f, nil }
	if _, err := c.Realtime.Subscribe(context.Background(), "notes", nil); err == nil || !f.isClosed() {
		t.Fatal("refusal accepted or leaked socket")
	}
}

func TestRealtimeChannelEncodesFiltersAndColumns(t *testing.T) {
	cases := []struct {
		opts *SubscribeOptions
		want string
	}{
		{nil, "table:notes"},
		{&SubscribeOptions{}, "table:notes"},
		{&SubscribeOptions{Filters: []RealtimeFilter{{Field: "owner_id", Value: "u 1&x"}}}, "table:notes?owner_id.eq=u%201%26x"},
		{&SubscribeOptions{Filters: []RealtimeFilter{{Field: "status", Operator: "in", Value: []string{"a", "b"}}, {Field: "n", Operator: "gte", Value: 3}}, Columns: []string{"id", "body"}}, "table:notes?status.in=a%2Cb&n.gte=3&columns=id,body"},
	}
	for _, c := range cases {
		got, err := tableChannel("notes", c.opts)
		if err != nil || got != c.want {
			t.Fatalf("got %q, %v; want %q", got, err, c.want)
		}
	}
	for _, bad := range []*SubscribeOptions{
		{Filters: []RealtimeFilter{{Field: "a;b", Value: "x"}}},
		{Filters: []RealtimeFilter{{Field: "a", Operator: "between", Value: "x"}}},
		{Filters: []RealtimeFilter{{Field: "a"}}},
		{Columns: []string{"a b"}},
	} {
		if _, err := tableChannel("notes", bad); !errors.Is(err, ErrInvalidArg) {
			t.Fatalf("accepted %+v: %v", bad, err)
		}
	}
}

func TestRealtimeSubscribeSendsFilteredChannel(t *testing.T) {
	c := parityClient(t, nil)
	channel := "table:notes?owner_id.eq=u1"
	f := &fakeSocket{frames: []RealtimeEvent{{Type: "welcome"}, {Type: "subscribed", Channel: channel}}}
	c.Realtime.dial = func(context.Context, string) (realtimeSocket, error) { return f, nil }
	sub, err := c.Realtime.Subscribe(context.Background(), "notes", &SubscribeOptions{Filters: []RealtimeFilter{{Field: "owner_id", Value: "u1"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sent[1]["type"] != "subscribe" || f.sent[1]["channel"] != channel {
		t.Fatal(f.sent)
	}
}

// timeoutError is a read deadline passing, as net.Conn reports it.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// stalledSocket accepts writes and blocks every read until it is closed.
type stalledSocket struct {
	once   sync.Once
	closed chan struct{}
}

func (s *stalledSocket) WriteJSON(any) error             { return nil }
func (s *stalledSocket) SetReadDeadline(time.Time) error { return nil }
func (s *stalledSocket) ReadJSON(any) error {
	<-s.closed
	return errors.New("use of closed network connection")
}
func (s *stalledSocket) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func subscribed(t *testing.T, c *Client, f *fakeSocket) *Subscription {
	t.Helper()
	f.frames = append([]RealtimeEvent{{Type: "welcome"}, {Type: "subscribed", Channel: "table:notes"}}, f.frames...)
	c.Realtime.dial = func(context.Context, string) (realtimeSocket, error) { return f, nil }
	sub, err := c.Realtime.Subscribe(context.Background(), "notes", nil)
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// A dropped subscription is a *Error matching ErrNetwork, and the socket's own
// error stays reachable through errors.Is.
func TestRealtimeNextWrapsASocketError(t *testing.T) {
	sub := subscribed(t, parityClient(t, nil), &fakeSocket{})
	_, err := sub.Next(context.Background())
	var sdkErr *Error
	if !errors.As(err, &sdkErr) || !errors.Is(err, ErrNetwork) || errors.Is(err, ErrTimeout) || !errors.Is(err, io.EOF) {
		t.Fatalf("Next after the socket closed = %#v; want a *Error matching ErrNetwork (not ErrTimeout) that wraps io.EOF", err)
	}
}

func TestRealtimeNextReportsASilentSocketAsATimeout(t *testing.T) {
	sub := subscribed(t, parityClient(t, nil), &fakeSocket{end: timeoutError{}})
	_, err := sub.Next(context.Background())
	var sdkErr *Error
	if !errors.As(err, &sdkErr) || !errors.Is(err, ErrTimeout) || !errors.Is(err, ErrNetwork) {
		t.Fatalf("Next after a read deadline = %#v; want a *Error matching ErrTimeout and ErrNetwork", err)
	}
}

func TestRealtimeNextWrapsACanceledContext(t *testing.T) {
	sub := subscribed(t, parityClient(t, nil), &fakeSocket{})
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := sub.Next(stopped)
	var sdkErr *Error
	if !errors.As(err, &sdkErr) || !errors.Is(err, context.Canceled) || !errors.Is(err, ErrNetwork) {
		t.Fatalf("Next with a canceled context = %#v; want a *Error matching ErrNetwork that wraps context.Canceled", err)
	}
}

func TestRealtimeSubscribeWrapsASocketError(t *testing.T) {
	c := parityClient(t, nil)
	f := &fakeSocket{} // closes before the welcome frame
	c.Realtime.dial = func(context.Context, string) (realtimeSocket, error) { return f, nil }
	_, err := c.Realtime.Subscribe(context.Background(), "notes", nil)
	var sdkErr *Error
	if !errors.As(err, &sdkErr) || !errors.Is(err, ErrNetwork) || !errors.Is(err, io.EOF) {
		t.Fatalf("Subscribe when the socket closes = %#v; want a *Error matching ErrNetwork that wraps io.EOF", err)
	}
	if !f.isClosed() {
		t.Fatal("socket not closed")
	}
}

// Rule 8: a dial that fails (DNS, TCP, TLS or the WebSocket handshake) is a
// *Error matching ErrNetwork, not the transport's own error.
func TestRealtimeSubscribeWrapsADialError(t *testing.T) {
	c := parityClient(t, nil)
	refused := errors.New("connection refused")
	c.Realtime.dial = func(context.Context, string) (realtimeSocket, error) { return nil, refused }
	sub, err := c.Realtime.Subscribe(context.Background(), "notes", nil)
	if sub != nil {
		t.Fatal("Subscribe returned a subscription for a failed dial")
	}
	var sdkErr *Error
	if !errors.As(err, &sdkErr) || !errors.Is(err, ErrNetwork) || !errors.Is(err, refused) {
		t.Fatalf("Subscribe when the dial fails = %#v; want a *Error matching ErrNetwork that wraps the dial error", err)
	}
	if sdkErr.Message != "could not reach the VAIF API" {
		t.Fatalf("Message = %q; want %q", sdkErr.Message, "could not reach the VAIF API")
	}
}

func TestRealtimeSubscribeTimesOutWithoutAnAcknowledgment(t *testing.T) {
	c, err := NewClient("project", "vk_srv_test", WithTimeout(20*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	s := &stalledSocket{closed: make(chan struct{})}
	c.Realtime.dial = func(context.Context, string) (realtimeSocket, error) { return s, nil }
	_, err = c.Realtime.Subscribe(context.Background(), "notes", nil)
	var sdkErr *Error
	if !errors.As(err, &sdkErr) || !errors.Is(err, ErrTimeout) {
		t.Fatalf("Subscribe with no welcome = %#v; want a *Error matching ErrTimeout", err)
	}
}
