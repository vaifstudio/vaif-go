package vaif

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// AI provides AI-powered code generation for schemas, functions, endpoints,
// and interactive chat. All methods require a context.Context.
type AI struct {
	client *Client
}

// GenerateSchema generates a database schema from natural language.
//
//	result, err := client.AI.GenerateSchema(ctx, vaif.SchemaGenerationRequest{
//	    Prompt: "Create a blog with posts, authors, and comments",
//	    Format: "sql",
//	}, nil)
func (a *AI) GenerateSchema(ctx context.Context, req SchemaGenerationRequest, opts *AIGenerateOptions) (*SchemaGenerationResult, error) {
	body := a.buildBody(req, opts)

	raw, err := a.client.request(ctx, "POST", "/ai/generate/schema", body, nil)
	if err != nil {
		return nil, err
	}

	resp := parseResponse[SchemaGenerationResult](raw)
	if resp.Error != nil {
		return nil, &AIError{Message: resp.Error.Message, Code: resp.Error.Code, Status: resp.Status, sentinel: resp.Error.sentinel}
	}

	return &resp.Data, nil
}

// GenerateFunction generates an edge function from natural language.
func (a *AI) GenerateFunction(ctx context.Context, req FunctionGenerationRequest, opts *AIGenerateOptions) (*FunctionGenerationResult, error) {
	body := a.buildBody(req, opts)

	raw, err := a.client.request(ctx, "POST", "/ai/generate/function", body, nil)
	if err != nil {
		return nil, err
	}

	resp := parseResponse[FunctionGenerationResult](raw)
	if resp.Error != nil {
		return nil, &AIError{Message: resp.Error.Message, Code: resp.Error.Code, Status: resp.Status, sentinel: resp.Error.sentinel}
	}

	return &resp.Data, nil
}

// GenerateEndpoint generates an API endpoint from natural language.
func (a *AI) GenerateEndpoint(ctx context.Context, req EndpointGenerationRequest, opts *AIGenerateOptions) (*EndpointGenerationResult, error) {
	body := a.buildBody(req, opts)

	raw, err := a.client.request(ctx, "POST", "/ai/generate/endpoint", body, nil)
	if err != nil {
		return nil, err
	}

	resp := parseResponse[EndpointGenerationResult](raw)
	if resp.Error != nil {
		return nil, &AIError{Message: resp.Error.Message, Code: resp.Error.Code, Status: resp.Status, sentinel: resp.Error.sentinel}
	}

	return &resp.Data, nil
}

// Chat sends a chat request to the AI assistant and returns the full response.
func (a *AI) Chat(ctx context.Context, req AIChatRequest, opts *AIGenerateOptions) (*AIChatResponse, error) {
	body := a.buildBody(req, opts)

	raw, err := a.client.request(ctx, "POST", "/ai/chat", body, nil)
	if err != nil {
		return nil, err
	}

	resp := parseResponse[AIChatResponse](raw)
	if resp.Error != nil {
		return nil, &AIError{Message: resp.Error.Message, Code: resp.Error.Code, Status: resp.Status, sentinel: resp.Error.sentinel}
	}

	return &resp.Data, nil
}

// ChatStream streams a chat response via Server-Sent Events. The caller must
// call Close on the returned StreamReader when done. The stream respects
// context cancellation.
//
//	stream, err := client.AI.ChatStream(ctx, req, nil)
//	if err != nil { ... }
//	defer stream.Close()
//	for {
//	    chunk, err := stream.Read()
//	    if err == io.EOF { break }
//	    if err != nil { ... }
//	    fmt.Print(chunk)
//	}
func (a *AI) ChatStream(ctx context.Context, req AIChatRequest, opts *AIGenerateOptions) (*StreamReader, error) {
	body := a.buildBody(req, opts)
	body["stream"] = true

	config := a.client.GetConfig()
	url := fmt.Sprintf("%s/ai/chat/stream", config.APIURL)

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal chat body: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("X-Project-ID", config.ProjectID)
	httpReq.Header.Set("apikey", config.APIKey)

	for k, v := range config.Headers {
		httpReq.Header.Set(k, v)
	}

	if token := a.client.GetAccessToken(); token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := a.client.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("chat stream request: %w", err)
	}

	if resp.StatusCode >= 300 {
		resp.Body.Close()
		sentinel := sentinelForStatus(resp.StatusCode)
		return nil, &AIError{
			Message:  fmt.Sprintf("AI chat stream failed with status %d", resp.StatusCode),
			Status:   resp.StatusCode,
			sentinel: sentinel,
		}
	}

	return &StreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
		ctx:    ctx,
	}, nil
}

// StreamReader reads SSE chunks from the AI chat stream. It is not safe for
// concurrent use.
type StreamReader struct {
	reader *bufio.Reader
	body   io.ReadCloser
	ctx    context.Context
}

// Read returns the next content chunk from the stream. Returns io.EOF when
// the stream is complete. Respects context cancellation.
func (s *StreamReader) Read() (string, error) {
	for {
		// Check context cancellation
		select {
		case <-s.ctx.Done():
			return "", s.ctx.Err()
		default:
		}

		line, err := s.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return "", io.EOF
			}
			return "", fmt.Errorf("read stream: %w", err)
		}

		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := line[6:]
		if data == "[DONE]" {
			return "", io.EOF
		}

		var parsed map[string]any
		if err := json.Unmarshal([]byte(data), &parsed); err != nil {
			continue
		}

		if content, ok := parsed["content"].(string); ok {
			return content, nil
		}
	}
}

// Close closes the underlying HTTP response body.
func (s *StreamReader) Close() error {
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}

// GetCredits returns AI credits information for the current project.
func (a *AI) GetCredits(ctx context.Context) (*AICreditsInfo, error) {
	raw, err := a.client.request(ctx, "GET", "/ai/credits", nil, nil)
	if err != nil {
		return nil, err
	}

	resp := parseResponse[AICreditsInfo](raw)
	if resp.Error != nil {
		return nil, &AIError{Message: resp.Error.Message, Code: resp.Error.Code, Status: resp.Status, sentinel: resp.Error.sentinel}
	}

	return &resp.Data, nil
}

// Explain asks the AI to explain code or schema and returns structured analysis.
func (a *AI) Explain(ctx context.Context, code string, language, extraContext string, opts *AIGenerateOptions) (map[string]any, error) {
	body := map[string]any{"code": code}
	if language != "" {
		body["language"] = language
	}
	if extraContext != "" {
		body["context"] = extraContext
	}
	a.applyOpts(body, opts)

	raw, err := a.client.request(ctx, "POST", "/ai/explain", body, nil)
	if err != nil {
		return nil, err
	}

	resp := parseResponse[map[string]any](raw)
	if resp.Error != nil {
		return nil, &AIError{Message: resp.Error.Message, Code: resp.Error.Code, Status: resp.Status, sentinel: resp.Error.sentinel}
	}

	return resp.Data, nil
}

// Review asks the AI to review code for issues, security vulnerabilities,
// and potential improvements.
func (a *AI) Review(ctx context.Context, code string, language string, focus []string, opts *AIGenerateOptions) (map[string]any, error) {
	body := map[string]any{"code": code}
	if language != "" {
		body["language"] = language
	}
	if len(focus) > 0 {
		body["focus"] = focus
	}
	a.applyOpts(body, opts)

	raw, err := a.client.request(ctx, "POST", "/ai/review", body, nil)
	if err != nil {
		return nil, err
	}

	resp := parseResponse[map[string]any](raw)
	if resp.Error != nil {
		return nil, &AIError{Message: resp.Error.Message, Code: resp.Error.Code, Status: resp.Status, sentinel: resp.Error.sentinel}
	}

	return resp.Data, nil
}

// GenerateTypes generates TypeScript types from a schema definition.
func (a *AI) GenerateTypes(ctx context.Context, schema, format string, opts *AIGenerateOptions) (map[string]any, error) {
	body := map[string]any{
		"schema": schema,
		"format": format,
	}
	a.applyOpts(body, opts)

	raw, err := a.client.request(ctx, "POST", "/ai/generate/types", body, nil)
	if err != nil {
		return nil, err
	}

	resp := parseResponse[map[string]any](raw)
	if resp.Error != nil {
		return nil, &AIError{Message: resp.Error.Message, Code: resp.Error.Code, Status: resp.Status, sentinel: resp.Error.sentinel}
	}

	return resp.Data, nil
}

// GenerateMigration generates a SQL migration from a schema diff.
func (a *AI) GenerateMigration(ctx context.Context, currentSchema, targetSchema, database string, opts *AIGenerateOptions) (map[string]any, error) {
	body := map[string]any{
		"currentSchema": currentSchema,
		"targetSchema":  targetSchema,
		"database":      database,
	}
	a.applyOpts(body, opts)

	raw, err := a.client.request(ctx, "POST", "/ai/generate/migration", body, nil)
	if err != nil {
		return nil, err
	}

	resp := parseResponse[map[string]any](raw)
	if resp.Error != nil {
		return nil, &AIError{Message: resp.Error.Message, Code: resp.Error.Code, Status: resp.Status, sentinel: resp.Error.sentinel}
	}

	return resp.Data, nil
}

func (a *AI) buildBody(req any, opts *AIGenerateOptions) map[string]any {
	// Marshal the struct to JSON, then unmarshal to a map for flexible merging
	jsonBytes, err := json.Marshal(req)
	if err != nil {
		return map[string]any{}
	}
	body := make(map[string]any)
	if err := json.Unmarshal(jsonBytes, &body); err != nil {
		return map[string]any{}
	}

	a.applyOpts(body, opts)

	// Remove zero-value keys to avoid sending unnecessary data
	for k, v := range body {
		switch tv := v.(type) {
		case nil:
			delete(body, k)
		case string:
			if tv == "" {
				delete(body, k)
			}
		case bool:
			if !tv {
				delete(body, k)
			}
		case float64:
			if tv == 0 {
				delete(body, k)
			}
		}
	}

	return body
}

func (a *AI) applyOpts(body map[string]any, opts *AIGenerateOptions) {
	if opts == nil {
		return
	}
	if opts.Model != "" {
		body["model"] = string(opts.Model)
	}
	if opts.Temperature != nil {
		body["temperature"] = *opts.Temperature
	}
	if opts.MaxTokens > 0 {
		body["maxTokens"] = opts.MaxTokens
	}
}
