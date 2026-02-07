package vaif

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Functions provides edge function invocation and management.
// All methods require a context.Context for cancellation and timeout support.
type Functions struct {
	client *Client
}

// Invoke calls an edge function by name with the given body and options.
// The function response content type determines how the data is returned:
// JSON is unmarshalled, text is returned as string, binary as []byte.
//
//	result, err := client.Functions.Invoke(ctx, "send-email", map[string]any{
//	    "to": "user@example.com",
//	    "subject": "Hello",
//	}, nil)
func (f *Functions) Invoke(ctx context.Context, name string, body any, opts *FunctionInvokeOptions) (*FunctionResponse[any], error) {
	if opts == nil {
		opts = &FunctionInvokeOptions{}
	}

	method := opts.Method
	if method == "" {
		method = "POST"
	}

	config := f.client.GetConfig()
	url := fmt.Sprintf("%s/functions/%s", config.APIURL, name)

	var bodyReader io.Reader
	if body != nil {
		jsonBytes, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal function body: %w", err)
		}
		bodyReader = strings.NewReader(string(jsonBytes))
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create function request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Project-ID", config.ProjectID)
	req.Header.Set("apikey", config.APIKey)

	for k, v := range config.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range opts.Headers {
		req.Header.Set(k, v)
	}

	if token := f.client.GetAccessToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	timeout := config.Timeout
	if opts.Timeout > 0 {
		timeout = opts.Timeout
	}

	httpClient := &http.Client{
		Timeout: time.Duration(timeout) * time.Millisecond,
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return &FunctionResponse[any]{
			Error: &FunctionError{Message: err.Error(), Name: name, sentinel: ErrTimeout},
		}, nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return &FunctionResponse[any]{
			Error: &FunctionError{Message: "failed to read response", Name: name},
		}, nil
	}

	contentType := resp.Header.Get("Content-Type")

	var data any
	if strings.HasPrefix(contentType, "application/json") {
		json.Unmarshal(respBody, &data)
	} else if strings.HasPrefix(contentType, "text/") {
		data = string(respBody)
	} else {
		data = respBody
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return &FunctionResponse[any]{Data: data}, nil
	}

	msg := fmt.Sprintf("Function invocation failed: %d", resp.StatusCode)
	if m, ok := data.(map[string]any); ok {
		if v, ok := m["message"].(string); ok {
			msg = v
		}
	}

	return &FunctionResponse[any]{
		Error: &FunctionError{Message: msg, Status: resp.StatusCode, Name: name, sentinel: sentinelForStatus(resp.StatusCode)},
	}, nil
}

// CreateURL returns the URL for direct function invocation (e.g. for webhooks).
func (f *Functions) CreateURL(name string) string {
	return fmt.Sprintf("%s/functions/%s", f.client.config.APIURL, name)
}

// List returns all deployed functions.
func (f *Functions) List(ctx context.Context) ([]map[string]any, error) {
	raw, err := f.client.request(ctx, "GET", "/functions", nil, nil)
	if err != nil {
		return nil, err
	}

	resp := parseResponse[[]map[string]any](raw)
	if resp.Error != nil {
		return nil, &FunctionError{
			Message:  resp.Error.Message,
			Status:   resp.Status,
			Name:     "list",
			sentinel: resp.Error.sentinel,
		}
	}

	return resp.Data, nil
}

// Get returns details for a specific deployed function.
func (f *Functions) Get(ctx context.Context, name string) (map[string]any, error) {
	raw, err := f.client.request(ctx, "GET", "/functions/"+name, nil, nil)
	if err != nil {
		return nil, err
	}

	resp := parseResponse[map[string]any](raw)
	if resp.Error != nil {
		return nil, &FunctionError{
			Message:  resp.Error.Message,
			Status:   resp.Status,
			Name:     name,
			sentinel: resp.Error.sentinel,
		}
	}

	return resp.Data, nil
}
