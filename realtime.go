package vaif

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Realtime provides WebSocket-based realtime subscriptions for database changes,
// broadcasts, and presence. Safe for concurrent use.
type Realtime struct {
	client            *Client
	conn              *websocket.Conn
	channels          map[string]*Channel
	connState         ConnectionState
	reconnectAttempts int
	options           RealtimeOptions
	done              chan struct{}
	mu                sync.RWMutex
	writeMu           sync.Mutex // serializes writes to the WebSocket
}

func newRealtime(c *Client) *Realtime {
	return &Realtime{
		client:    c,
		channels:  make(map[string]*Channel),
		options:   DefaultRealtimeOptions(),
		connState: ConnectionDisconnected,
	}
}

// Connect establishes a WebSocket connection to the realtime server.
// Pass nil opts to use DefaultRealtimeOptions.
func (r *Realtime) Connect(opts *RealtimeOptions) error {
	r.mu.Lock()
	if r.connState == ConnectionConnected && r.conn != nil {
		r.mu.Unlock()
		return nil
	}

	if opts != nil {
		r.options = *opts
	}

	r.connState = ConnectionConnecting
	r.mu.Unlock()

	r.notifyConnectionState(ConnectionConnecting)

	config := r.client.GetConfig()
	// Build WebSocket URL with proper URL encoding to prevent injection
	wsURL, err := url.Parse(config.RealtimeURL)
	if err != nil {
		return fmt.Errorf("invalid realtime URL: %w", err)
	}
	q := wsURL.Query()
	q.Set("apikey", config.APIKey)
	q.Set("project", config.ProjectID)
	if token := r.client.GetAccessToken(); token != "" {
		q.Set("token", token)
	}
	wsURL.RawQuery = q.Encode()

	// Log connection attempt without credentials
	r.client.debug("Realtime connecting to %s", config.RealtimeURL)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	conn, _, err := dialer.Dial(wsURL.String(), nil)
	if err != nil {
		r.client.debug("Realtime connection failed")
		r.mu.Lock()
		r.connState = ConnectionDisconnected
		r.mu.Unlock()
		r.notifyConnectionState(ConnectionDisconnected)
		r.notifyError(fmt.Errorf("websocket dial failed"))
		return fmt.Errorf("websocket dial failed: %w", err)
	}

	r.mu.Lock()
	r.conn = conn
	r.connState = ConnectionConnected
	r.reconnectAttempts = 0
	r.done = make(chan struct{})

	// Set up ping/pong handler for keepalive
	conn.SetPongHandler(func(appData string) error {
		r.client.debug("received pong")
		return nil
	})

	r.mu.Unlock()

	r.notifyConnectionState(ConnectionConnected)
	r.client.debug("Realtime connected")

	// Start heartbeat with ping/pong
	if r.options.Heartbeat {
		go r.heartbeatLoop()
	}

	// Start receive loop
	go r.receiveLoop()

	// Resubscribe existing channels
	r.mu.RLock()
	channels := make([]*Channel, 0, len(r.channels))
	for _, ch := range r.channels {
		channels = append(channels, ch)
	}
	r.mu.RUnlock()

	for _, ch := range channels {
		ch.resubscribe()
	}

	return nil
}

// Disconnect closes the WebSocket connection and cleans up all channels
// and goroutines.
func (r *Realtime) Disconnect() error {
	r.mu.Lock()

	r.connState = ConnectionDisconnected

	if r.done != nil {
		select {
		case <-r.done:
			// Already closed
		default:
			close(r.done)
		}
		r.done = nil
	}

	// Unsubscribe all channels
	channels := make([]*Channel, 0, len(r.channels))
	for _, ch := range r.channels {
		channels = append(channels, ch)
	}
	r.channels = make(map[string]*Channel)

	conn := r.conn
	r.conn = nil
	r.mu.Unlock()

	// Unsubscribe outside the lock to avoid deadlock
	for _, ch := range channels {
		ch.unsubscribe()
	}

	r.notifyConnectionState(ConnectionDisconnected)

	if conn != nil {
		return conn.Close()
	}

	return nil
}

// Channel creates or returns an existing channel by name.
func (r *Realtime) Channel(name string) *Channel {
	r.mu.Lock()
	defer r.mu.Unlock()

	if ch, ok := r.channels[name]; ok {
		return ch
	}

	ch := newChannel(r, name)
	r.channels[name] = ch
	return ch
}

// RemoveChannel removes and unsubscribes a channel.
func (r *Realtime) RemoveChannel(name string) {
	r.mu.Lock()
	ch, ok := r.channels[name]
	if ok {
		delete(r.channels, name)
	}
	r.mu.Unlock()

	if ok {
		ch.unsubscribe()
	}
}

// IsConnected returns true if the WebSocket connection is active.
func (r *Realtime) IsConnected() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.connState == ConnectionConnected
}

// State returns the current connection state.
func (r *Realtime) State() ConnectionState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.connState
}

func (r *Realtime) send(msg any) error {
	r.mu.RLock()
	conn := r.conn
	connected := r.connState == ConnectionConnected
	r.mu.RUnlock()

	if conn == nil || !connected {
		return fmt.Errorf("not connected")
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	// Serialize writes to prevent concurrent write panics
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return conn.WriteMessage(websocket.TextMessage, data)
}

func (r *Realtime) heartbeatLoop() {
	r.mu.RLock()
	done := r.done
	interval := time.Duration(r.options.HeartbeatInterval) * time.Millisecond
	r.mu.RUnlock()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			// Send both a WebSocket ping and an application-level heartbeat
			r.mu.RLock()
			conn := r.conn
			r.mu.RUnlock()

			if conn != nil {
				r.writeMu.Lock()
				err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
				r.writeMu.Unlock()

				if err != nil {
					r.client.debug("ping failed: %v", err)
				}
			}

			r.send(map[string]any{
				"topic":   "phoenix",
				"event":   "heartbeat",
				"payload": map[string]any{},
			})
		}
	}
}

func (r *Realtime) receiveLoop() {
	r.mu.RLock()
	done := r.done
	r.mu.RUnlock()

	for {
		select {
		case <-done:
			return
		default:
		}

		r.mu.RLock()
		conn := r.conn
		r.mu.RUnlock()

		if conn == nil {
			return
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			// Check if this is a deliberate close
			select {
			case <-done:
				return
			default:
			}
			r.client.debug("Receive error: %v", err)
			r.notifyError(err)
			r.handleDisconnect()
			return
		}

		var msg map[string]any
		if err := json.Unmarshal(message, &msg); err != nil {
			continue
		}

		topic, _ := msg["topic"].(string)
		r.mu.RLock()
		ch, ok := r.channels[topic]
		r.mu.RUnlock()

		if ok {
			ch.handleMessage(msg)
		}
	}
}

func (r *Realtime) handleDisconnect() {
	r.mu.Lock()
	wasConnected := r.connState == ConnectionConnected || r.connState == ConnectionReconnecting
	r.connState = ConnectionDisconnected

	// Close the old connection if still set (prevent leak)
	conn := r.conn
	r.conn = nil
	opts := r.options
	r.mu.Unlock()

	if conn != nil {
		conn.Close()
	}

	if wasConnected {
		r.notifyConnectionState(ConnectionDisconnected)
	}

	r.client.debug("Realtime disconnected")

	if !opts.AutoReconnect {
		return
	}

	r.mu.RLock()
	attempts := r.reconnectAttempts
	r.mu.RUnlock()

	if attempts >= opts.MaxReconnectAttempts {
		r.client.debug("Max reconnect attempts reached (%d)", opts.MaxReconnectAttempts)
		return
	}

	r.mu.Lock()
	r.reconnectAttempts++
	r.connState = ConnectionReconnecting
	attempt := r.reconnectAttempts
	r.mu.Unlock()

	r.notifyConnectionState(ConnectionReconnecting)

	// Exponential backoff with jitter
	baseDelay := time.Duration(opts.ReconnectInterval) * time.Millisecond
	delay := baseDelay * time.Duration(1<<uint(attempt-1))
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	// Add jitter: 0-25% of delay (guard against zero delay)
	if delay > 0 {
		jitter := time.Duration(rand.Int63n(int64(delay) / 4))
		delay += jitter
	}

	r.client.debug("Reconnecting in %v (attempt %d/%d)", delay, attempt, opts.MaxReconnectAttempts)
	time.Sleep(delay)

	if err := r.Connect(nil); err != nil {
		r.client.debug("Reconnect attempt %d failed: %v", attempt, err)
	}
}

func (r *Realtime) notifyConnectionState(state ConnectionState) {
	r.mu.RLock()
	opts := r.options
	r.mu.RUnlock()

	switch state {
	case ConnectionConnected:
		if opts.OnConnect != nil {
			opts.OnConnect(state)
		}
	case ConnectionDisconnected:
		if opts.OnDisconnect != nil {
			opts.OnDisconnect(state)
		}
	default:
		// OnConnect/OnDisconnect for transitional states
	}
}

func (r *Realtime) notifyError(err error) {
	r.mu.RLock()
	onError := r.options.OnError
	r.mu.RUnlock()

	if onError != nil {
		onError(err)
	}
}

// SubscriptionCallback is called with event payloads.
type SubscriptionCallback func(payload any)

// Channel represents a realtime channel for subscriptions. Supports database
// change events, broadcast messages, and presence tracking.
type Channel struct {
	realtime      *Realtime
	name          string
	subscriptions map[string][]SubscriptionCallback
	status        ChannelStatus
	mu            sync.RWMutex
}

func newChannel(r *Realtime, name string) *Channel {
	return &Channel{
		realtime:      r,
		name:          name,
		subscriptions: make(map[string][]SubscriptionCallback),
		status:        ChannelClosed,
	}
}

// On subscribes to events matching the given event type and filter.
func (c *Channel) On(event string, filter map[string]any, callback SubscriptionCallback) *Channel {
	filterJSON, _ := json.Marshal(filter)
	key := fmt.Sprintf("%s:%s", event, string(filterJSON))

	c.mu.Lock()
	c.subscriptions[key] = append(c.subscriptions[key], callback)
	c.mu.Unlock()

	return c
}

// OnPostgresChanges subscribes to database change events. Event can be
// "INSERT", "UPDATE", "DELETE", or "*" for all.
func (c *Channel) OnPostgresChanges(event, schema, table string, callback SubscriptionCallback) *Channel {
	filter := map[string]any{
		"event":  event,
		"schema": schema,
	}
	if table != "" {
		filter["table"] = table
	}
	return c.On("postgres_changes", filter, callback)
}

// OnBroadcast subscribes to broadcast events on this channel.
func (c *Channel) OnBroadcast(event string, callback SubscriptionCallback) *Channel {
	return c.On("broadcast", map[string]any{"event": event}, callback)
}

// OnPresence subscribes to presence events (sync, join, leave).
func (c *Channel) OnPresence(event string, callback SubscriptionCallback) *Channel {
	return c.On("presence", map[string]any{"event": event}, callback)
}

// Subscribe sends the subscription to the server and activates the channel.
func (c *Channel) Subscribe(callback func(status ChannelStatus, err error)) *Channel {
	c.mu.RLock()
	keys := make([]string, 0, len(c.subscriptions))
	for k := range c.subscriptions {
		keys = append(keys, k)
	}
	c.mu.RUnlock()

	err := c.realtime.send(map[string]any{
		"topic":   c.name,
		"event":   "phx_join",
		"payload": map[string]any{"subscriptions": keys},
		"ref":     c.name + "-join",
	})

	c.mu.Lock()
	if err != nil {
		c.status = ChannelErrored
	} else {
		c.status = ChannelSubscribed
	}
	status := c.status
	c.mu.Unlock()

	if callback != nil {
		callback(status, err)
	}

	return c
}

// Unsubscribe unsubscribes from the channel and clears all callbacks.
func (c *Channel) Unsubscribe() {
	c.unsubscribe()
}

// Status returns the current channel subscription status.
func (c *Channel) Status() ChannelStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

func (c *Channel) unsubscribe() {
	c.realtime.send(map[string]any{
		"topic":   c.name,
		"event":   "phx_leave",
		"payload": map[string]any{},
		"ref":     c.name + "-leave",
	})

	c.mu.Lock()
	c.status = ChannelClosed
	c.subscriptions = make(map[string][]SubscriptionCallback)
	c.mu.Unlock()
}

// Broadcast sends a message to all subscribers on this channel.
func (c *Channel) Broadcast(event string, payload any) error {
	return c.realtime.send(map[string]any{
		"topic": c.name,
		"event": "broadcast",
		"payload": map[string]any{
			"event":   event,
			"payload": payload,
		},
	})
}

// Track tracks presence in the channel with the given payload.
func (c *Channel) Track(payload map[string]any) error {
	return c.realtime.send(map[string]any{
		"topic": c.name,
		"event": "presence",
		"payload": map[string]any{
			"type":    "track",
			"payload": payload,
		},
	})
}

// Untrack removes presence tracking for this connection.
func (c *Channel) Untrack() error {
	return c.realtime.send(map[string]any{
		"topic": c.name,
		"event": "presence",
		"payload": map[string]any{
			"type": "untrack",
		},
	})
}

func (c *Channel) handleMessage(msg map[string]any) {
	event, _ := msg["event"].(string)
	payload := msg["payload"]

	c.mu.RLock()
	defer c.mu.RUnlock()

	for key, callbacks := range c.subscriptions {
		if event == "broadcast" || keyMatchesEvent(key, event) {
			for _, cb := range callbacks {
				func() {
					defer func() { recover() }()
					cb(payload)
				}()
			}
		}
	}
}

func keyMatchesEvent(key, event string) bool {
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			return key[:i] == event
		}
	}
	return key == event
}

func (c *Channel) resubscribe() {
	c.mu.RLock()
	hasSubs := len(c.subscriptions) > 0
	c.mu.RUnlock()

	if hasSubs {
		c.Subscribe(nil)
	}
}
