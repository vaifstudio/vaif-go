// Package vaif provides the official Go SDK for VAIF Studio BaaS.
//
// Create a client with NewClient and access sub-services via the client:
//
//	client := vaif.NewClient("project-id", "api-key")
//	result, err := client.DB.From("users").Select("*").Execute(ctx)
package vaif

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client is the main VAIF Studio client. It holds configuration and provides
// access to database, auth, storage, functions, realtime, and AI sub-clients.
// Client is safe for concurrent use.
type Client struct {
	config      VaifConfig
	accessToken string
	httpClient  *http.Client
	logger      Logger

	// Sub-clients (initialized in NewClient)
	DB        *Database
	Auth      *Auth
	Storage   *Storage
	Functions *Functions
	Realtime  *Realtime
	AI        *AI
}

// NewClient creates a new VAIF client with the given project ID and API key.
// Use functional options to customize behavior:
//
//	client := vaif.NewClient("project-id", "api-key",
//	    vaif.WithTimeout(10000),
//	    vaif.WithRetry(3, 500*time.Millisecond),
//	    vaif.WithDebug(true),
//	)
func NewClient(projectID, apiKey string, opts ...Option) *Client {
	c := &Client{
		config: VaifConfig{
			ProjectID:        projectID,
			APIKey:           apiKey,
			APIURL:           "https://api.vaif.studio",
			RealtimeURL:      "wss://realtime.vaif.studio",
			Timeout:          30000,
			Headers:          make(map[string]string),
			AutoRefreshToken: true,
			PersistSession:   true,
			MaxRetries:       0,
			RetryBackoff:     500 * time.Millisecond,
			MaxRetryDelay:    30 * time.Second,
		},
	}

	// Apply options (they modify Client directly)
	for _, opt := range opts {
		opt(c)
	}

	// Set up HTTP client if not provided by WithHTTPClient
	if c.httpClient == nil {
		c.httpClient = &http.Client{
			Timeout: time.Duration(c.config.Timeout) * time.Millisecond,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		}
	}

	// Set default logger if debug enabled and no custom logger set
	if c.config.Debug && c.logger == nil {
		c.logger = log.Default()
	}

	// Initialize sub-clients
	c.DB = &Database{client: c}
	c.Auth = newAuth(c)
	c.Storage = &Storage{client: c}
	c.Functions = &Functions{client: c}
	c.Realtime = newRealtime(c)
	c.AI = &AI{client: c}

	return c
}

// Option configures the client. Options are applied in order during NewClient.
type Option func(*Client)

// WithAPIURL sets a custom API URL.
func WithAPIURL(apiURL string) Option {
	return func(c *Client) { c.config.APIURL = apiURL }
}

// WithRealtimeURL sets a custom WebSocket URL.
func WithRealtimeURL(wsURL string) Option {
	return func(c *Client) { c.config.RealtimeURL = wsURL }
}

// WithTimeout sets the request timeout in milliseconds.
func WithTimeout(ms int) Option {
	return func(c *Client) { c.config.Timeout = ms }
}

// WithHeaders sets custom headers for all requests.
func WithHeaders(headers map[string]string) Option {
	return func(c *Client) { c.config.Headers = headers }
}

// WithDebug enables debug logging using the standard logger.
func WithDebug(debug bool) Option {
	return func(c *Client) { c.config.Debug = debug }
}

// WithRetry configures automatic retry with exponential backoff for transient
// errors (network errors, HTTP 5xx, 429). The backoff parameter sets the
// initial delay between retries; subsequent delays double with jitter.
func WithRetry(maxRetries int, backoff time.Duration) Option {
	return func(c *Client) {
		c.config.MaxRetries = maxRetries
		c.config.RetryBackoff = backoff
	}
}

// WithMaxRetryDelay sets the maximum delay between retries. Defaults to 30s.
func WithMaxRetryDelay(d time.Duration) Option {
	return func(c *Client) { c.config.MaxRetryDelay = d }
}

// WithHTTPClient sets a custom http.Client for all requests. This overrides
// the default client with its connection pooling and timeout settings.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) { c.httpClient = httpClient }
}

// WithLogger sets a custom logger. The Logger interface requires only Printf.
func WithLogger(l Logger) Option {
	return func(c *Client) { c.logger = l }
}

// SetAccessToken sets the Bearer token for authenticated requests.
func (c *Client) SetAccessToken(token string) {
	c.accessToken = token
}

// GetAccessToken returns the current access token.
func (c *Client) GetAccessToken() string {
	return c.accessToken
}

// GetConfig returns the current configuration.
func (c *Client) GetConfig() VaifConfig {
	return c.config
}

// Close releases all resources held by the client, including any open
// WebSocket connections.
func (c *Client) Close() error {
	if c.Realtime != nil {
		return c.Realtime.Disconnect()
	}
	return nil
}

// request makes an authenticated API request with optional retry logic.
// It is the core HTTP method used by all sub-clients.
func (c *Client) request(ctx context.Context, method, path string, body any, params url.Values) (*apiRawResponse, error) {
	var lastErr error
	maxAttempts := 1 + c.config.MaxRetries

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := c.retryDelay(attempt)
			c.debug("retry attempt %d/%d after %v", attempt, c.config.MaxRetries, delay)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		raw, err := c.doRequest(ctx, method, path, body, params)
		if err != nil {
			// Network-level errors are retryable
			if c.isRetryable(0, err) && attempt < maxAttempts-1 {
				lastErr = err
				continue
			}
			return nil, err
		}

		// Check for retryable HTTP status codes
		if c.isRetryable(raw.StatusCode, nil) && attempt < maxAttempts-1 {
			lastErr = fmt.Errorf("HTTP %d", raw.StatusCode)

			// Honor Retry-After header
			if retryAfter := raw.Headers["retry-after"]; retryAfter != "" {
				if secs, err := strconv.Atoi(retryAfter); err == nil {
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(time.Duration(secs) * time.Second):
					}
					// Skip the normal backoff delay on next iteration since we already waited
					continue
				}
			}
			continue
		}

		return raw, nil
	}

	return nil, fmt.Errorf("request failed after %d attempts: %w", maxAttempts, lastErr)
}

// doRequest performs a single HTTP request without retry logic.
func (c *Client) doRequest(ctx context.Context, method, path string, body any, params url.Values) (*apiRawResponse, error) {
	// Build URL
	u := c.config.APIURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}

	// Build body
	var bodyReader io.Reader
	if body != nil {
		jsonBytes, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBytes)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Project-ID", c.config.ProjectID)
	req.Header.Set("apikey", c.config.APIKey)

	for k, v := range c.config.Headers {
		req.Header.Set(k, v)
	}

	if c.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}

	c.debug("%s %s", method, path)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	// Collect response headers
	respHeaders := make(map[string]string)
	for k := range resp.Header {
		respHeaders[strings.ToLower(k)] = resp.Header.Get(k)
	}

	return &apiRawResponse{
		StatusCode: resp.StatusCode,
		Body:       respBody,
		Headers:    respHeaders,
	}, nil
}

// isRetryable returns true if the request should be retried.
func (c *Client) isRetryable(statusCode int, err error) bool {
	if c.config.MaxRetries <= 0 {
		return false
	}
	if err != nil {
		// Retry on network errors, but not on context cancellation
		return !isContextError(err)
	}
	// Retry on 429 (rate limited) and 5xx (server errors)
	return statusCode == 429 || statusCode >= 500
}

// retryDelay computes exponential backoff with jitter.
func (c *Client) retryDelay(attempt int) time.Duration {
	delay := c.config.RetryBackoff
	for i := 1; i < attempt; i++ {
		delay *= 2
	}
	// Add jitter: +/- 25%
	jitter := time.Duration(rand.Int63n(int64(delay) / 2))
	delay = delay - delay/4 + jitter

	if delay > c.config.MaxRetryDelay {
		delay = c.config.MaxRetryDelay
	}
	return delay
}

// isContextError checks if an error is from context cancellation/deadline.
func isContextError(err error) bool {
	return err == context.Canceled || err == context.DeadlineExceeded
}

// apiRawResponse holds the raw HTTP response data.
type apiRawResponse struct {
	StatusCode int
	Body       []byte
	Headers    map[string]string
}

// parseResponse parses the raw response into an ApiResponse with sentinel
// error support.
func parseResponse[T any](raw *apiRawResponse) *ApiResponse[T] {
	result := &ApiResponse[T]{
		Status:  raw.StatusCode,
		Headers: raw.Headers,
	}

	if raw.StatusCode >= 200 && raw.StatusCode < 300 {
		var data T
		if err := json.Unmarshal(raw.Body, &data); err == nil {
			result.Data = data
		}
	} else {
		// Try to parse error from response body
		var errBody map[string]any
		if err := json.Unmarshal(raw.Body, &errBody); err == nil {
			msg, _ := errBody["message"].(string)
			if msg == "" {
				if e, ok := errBody["error"].(string); ok {
					msg = e
				}
			}
			if msg == "" {
				msg = fmt.Sprintf("Request failed with status %d", raw.StatusCode)
			}
			code, _ := errBody["code"].(string)
			reqID, _ := raw.Headers["x-request-id"]
			result.Error = &ApiError{
				Message:   msg,
				Code:      code,
				Status:    raw.StatusCode,
				RequestID: reqID,
				sentinel:  sentinelForStatus(raw.StatusCode),
			}
		} else {
			result.Error = &ApiError{
				Message:  fmt.Sprintf("Request failed with status %d", raw.StatusCode),
				Status:   raw.StatusCode,
				sentinel: sentinelForStatus(raw.StatusCode),
			}
		}
	}

	return result
}

func (c *Client) debug(format string, args ...any) {
	if c.logger != nil {
		c.logger.Printf("[vaif] "+format, args...)
	}
}
