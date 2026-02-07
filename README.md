# vaif-go

[![Go Reference](https://pkg.go.dev/badge/github.com/vaifllc/vaif-go.svg)](https://pkg.go.dev/github.com/vaifllc/vaif-go)
[![Go Report Card](https://goreportcard.com/badge/github.com/vaifllc/vaif-go)](https://goreportcard.com/report/github.com/vaifllc/vaif-go)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Official Go SDK for [VAIF Studio](https://vaif.studio) — the Backend-as-a-Service platform. Provides type-safe access to database, auth, storage, edge functions, realtime subscriptions, and AI generation.

## Installation

```bash
go get github.com/vaifllc/vaif-go
```

Requires **Go 1.21+**.

## Quick Start

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	vaif "github.com/vaifllc/vaif-go"
)

func main() {
	client := vaif.NewClient(
		"your-project-id",
		"your-api-key",
		vaif.WithRetry(3, 500*time.Millisecond),
	)
	defer client.Close()

	ctx := context.Background()

	// Query the database
	result, err := client.DB.From("users").Select("id, email, name").Execute(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Found %d users\n", len(result.Data))
}
```

## Features

| Module       | Description                                      |
|------------- |--------------------------------------------------|
| **Database** | Fluent query builder, filters, raw SQL, RPC      |
| **Auth**     | Email/password, OAuth, OTP, token auto-refresh   |
| **Storage**  | Upload, download, signed URLs, progress tracking |
| **Functions**| Invoke edge functions, list deployments          |
| **Realtime** | WebSocket channels, postgres changes, presence   |
| **AI**       | Schema/function/endpoint generation, chat stream |

## Configuration

Use functional options to customize the client:

```go
client := vaif.NewClient("project-id", "api-key",
	vaif.WithAPIURL("https://custom-api.example.com"),
	vaif.WithRealtimeURL("wss://custom-ws.example.com"),
	vaif.WithTimeout(15000),               // Request timeout in ms
	vaif.WithRetry(3, 500*time.Millisecond), // 3 retries, 500ms initial backoff
	vaif.WithMaxRetryDelay(10*time.Second),  // Cap retry delay at 10s
	vaif.WithHeaders(map[string]string{      // Custom headers
		"X-Custom": "value",
	}),
	vaif.WithDebug(true),                  // Enable debug logging
	vaif.WithLogger(myLogger),             // Custom logger (implements Printf)
	vaif.WithHTTPClient(customHTTPClient), // Custom http.Client
)
```

Retry logic uses exponential backoff with jitter and handles HTTP 429 (rate limited), 5xx (server errors), and network failures. The `Retry-After` header is honored when present.

## Database

### Query Builder

```go
ctx := context.Background()

// Select with filters, ordering, and pagination
result, err := client.DB.From("posts").
	Select("id, title, created_at").
	Eq("published", true).
	Order("created_at", false). // descending
	Limit(10).
	Offset(20).
	Execute(ctx)

// Insert
_, err = client.DB.From("posts").Insert(map[string]any{
	"title":   "Hello World",
	"content": "My first post",
}).Execute(ctx)

// Update
_, err = client.DB.From("posts").
	Update(map[string]any{"title": "Updated Title"}).
	Eq("id", "post-123").
	Execute(ctx)

// Delete
_, err = client.DB.From("posts").
	Delete().
	Eq("id", "post-123").
	Execute(ctx)

// Upsert with conflict resolution
_, err = client.DB.From("users").
	Upsert(map[string]any{
		"email": "john@example.com",
		"name":  "John Updated",
	}, "email").
	Returning("id, email, name").
	Execute(ctx)
```

### Typed Results with ExecuteInto

```go
type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

var users []User
err := client.DB.From("users").
	Select("id, email, name").
	ExecuteInto(ctx, &users)
```

### Single Row Queries

```go
// Single: exactly one row expected (error if 0 or >1)
single, err := client.DB.From("users").
	Select("*").
	Eq("id", "user-123").
	Single(ctx)

// MaybeSingle: zero or one row (nil Data if not found)
maybe, err := client.DB.From("users").
	Select("*").
	Eq("email", "john@example.com").
	MaybeSingle(ctx)
```

### Filters

```go
builder.Eq("status", "active")         // =
builder.Neq("role", "admin")           // !=
builder.Gt("age", 18)                  // >
builder.Gte("score", 100)              // >=
builder.Lt("price", 50)                // <
builder.Lte("quantity", 0)             // <=
builder.Like("name", "%john%")         // LIKE
builder.ILike("email", "%@gmail.com")  // ILIKE (case-insensitive)
builder.Is("deleted_at", nil)          // IS NULL
builder.In("status", []any{"active", "pending"})
builder.Contains("tags", []any{"go"})
builder.TextSearch("content", "hello world", nil)

// Negate any filter
builder.Not("status", "eq", "banned")

// OR conditions
builder.Or("age.gt.18,role.eq.admin")
```

### Raw SQL and RPC

```go
// Parameterized raw SQL
result, err := client.DB.Raw(ctx,
	"SELECT * FROM users WHERE age > $1 AND status = $2",
	18, "active",
)

// Call a stored procedure
rpcResult, err := client.DB.RPC(ctx, "get_user_stats", map[string]any{
	"user_id": "user-123",
})
```

## Auth

```go
ctx := context.Background()

// Sign up
resp, err := client.Auth.SignUp(ctx, vaif.SignUpOptions{
	Email:    "user@example.com",
	Password: "securepassword123",
	Data:     map[string]any{"display_name": "New User"},
})

// Sign in with email/password
resp, err = client.Auth.SignInWithPassword(ctx, vaif.SignInOptions{
	Email:    "user@example.com",
	Password: "password123",
})
// The client automatically stores the access token after sign-in

// OAuth sign-in (returns redirect URL)
url, err := client.Auth.SignInWithOAuth(vaif.OAuthGoogle, "https://myapp.com/callback", nil)

// OTP (magic link)
err = client.Auth.SignInWithOTP(ctx, vaif.OTPOptions{Email: "user@example.com"})
err = client.Auth.VerifyOTP(ctx, vaif.VerifyOTPOptions{
	Email: "user@example.com",
	Token: "123456",
	Type:  "email",
})

// Session management
err = client.Auth.RefreshSession(ctx)
err = client.Auth.SignOut(ctx)

// Password reset
err = client.Auth.ResetPasswordForEmail(ctx, "user@example.com", "https://myapp.com/reset")
```

### Auth State Changes

```go
unsub := client.Auth.OnAuthStateChange(func(event vaif.AuthChangeEvent, session *vaif.Session) {
	switch event {
	case vaif.AuthEventSignedIn:
		fmt.Println("User signed in:", session.User.Email)
	case vaif.AuthEventSignedOut:
		fmt.Println("User signed out")
	case vaif.AuthEventTokenRefreshed:
		fmt.Println("Token refreshed")
	}
})
defer unsub() // Stop listening
```

Token auto-refresh runs in a background goroutine and refreshes at 80% of token lifetime.

## Storage

```go
ctx := context.Background()
bucket := client.Storage.From("avatars")

// Upload from reader
result, err := bucket.Upload(ctx, "profile.jpg", file, &vaif.UploadOptions{
	ContentType: "image/jpeg",
	Upsert:      true,
})

// Upload with progress tracking
result, err = bucket.UploadWithProgress(ctx, "video.mp4", file, fileSize,
	&vaif.UploadOptions{ContentType: "video/mp4"},
	func(bytesUploaded, totalBytes int64) {
		fmt.Printf("Progress: %d%%\n", bytesUploaded*100/totalBytes)
	},
)

// Upload a local file directly
result, err = bucket.UploadFile(ctx, "doc.pdf", "/path/to/local/file.pdf", nil)

// Download
data, err := bucket.Download(ctx, "profile.jpg", nil)

// Download to a local file
err = bucket.DownloadToFile(ctx, "backup.sql", "/tmp/backup.sql", nil)

// Public URL
url := bucket.GetPublicURL("profile.jpg")

// Signed URL (expires in 1 hour)
signedURL, err := bucket.CreateSignedURL(ctx, "private/report.pdf", vaif.SignedURLOptions{
	ExpiresIn: 3600,
})

// List files
files, err := bucket.List(ctx, "uploads/", &vaif.ListFilesOptions{
	Limit:  100,
	Offset: 0,
	SortBy: "created_at",
})

// Move, copy, remove
err = bucket.Move(ctx, "old/path.jpg", "new/path.jpg")
err = bucket.Copy(ctx, "source.jpg", "destination.jpg")
err = bucket.Remove(ctx, []string{"file1.jpg", "file2.jpg"})
```

### Bucket Management

```go
buckets, err := client.Storage.ListBuckets(ctx)
err = client.Storage.CreateBucket(ctx, "my-bucket", map[string]any{"public": true})
err = client.Storage.DeleteBucket(ctx, "my-bucket")
```

## Functions

```go
ctx := context.Background()

// Invoke an edge function
result, err := client.Functions.Invoke(ctx, "send-email", map[string]any{
	"to":      "user@example.com",
	"subject": "Welcome!",
	"body":    "Thanks for signing up.",
}, nil)

// With custom options
result, err = client.Functions.Invoke(ctx, "process", data, &vaif.FunctionInvokeOptions{
	Method:  "PUT",
	Timeout: 60000,
	Headers: map[string]string{"X-Custom": "value"},
})

// Get function URL (e.g., for webhooks)
url := client.Functions.CreateURL("webhook-handler")

// List deployed functions
functions, err := client.Functions.List(ctx)
```

## Realtime

```go
// Connect with options
err := client.Realtime.Connect(&vaif.RealtimeOptions{
	Heartbeat:            true,
	HeartbeatInterval:    30000,
	AutoReconnect:        true,
	MaxReconnectAttempts: 10,
	OnConnect: func(state vaif.ConnectionState) {
		fmt.Println("Connected")
	},
	OnDisconnect: func(state vaif.ConnectionState) {
		fmt.Println("Disconnected")
	},
	OnError: func(err error) {
		fmt.Printf("Error: %v\n", err)
	},
})
defer client.Realtime.Disconnect()

// Subscribe to database changes
ch := client.Realtime.Channel("room:lobby")

ch.OnPostgresChanges("INSERT", "public", "messages", func(payload any) {
	fmt.Printf("New message: %v\n", payload)
})

ch.OnPostgresChanges("UPDATE", "public", "messages", func(payload any) {
	fmt.Printf("Updated: %v\n", payload)
})

ch.Subscribe(func(status vaif.ChannelStatus, err error) {
	fmt.Printf("Channel: %s\n", status)
})

// Broadcast to other clients
ch.Broadcast("typing", map[string]any{"user": "john"})

// Presence tracking
ch.Track(map[string]any{
	"user":      "john",
	"online_at": time.Now().Unix(),
})
ch.Untrack()
```

## AI

```go
ctx := context.Background()

// Generate a database schema
schema, err := client.AI.GenerateSchema(ctx, vaif.SchemaGenerationRequest{
	Prompt:           "Create a blog with posts, authors, and comments",
	Format:           "sql",
	IncludeRelations: true,
	IncludeIndexes:   true,
}, nil)
fmt.Printf("Generated %d tables using %d credits\n", len(schema.Tables), schema.CreditsUsed)

// Generate with model options
schema, err = client.AI.GenerateSchema(ctx, vaif.SchemaGenerationRequest{
	Prompt: "E-commerce product catalog",
	Format: "sql",
}, &vaif.AIGenerateOptions{
	Model:       vaif.CopilotPrecision,
	MaxTokens:   4096,
	Temperature: floatPtr(0.3),
})

// Stream a chat response
stream, err := client.AI.ChatStream(ctx, vaif.AIChatRequest{
	Messages: []vaif.AIChatMessage{
		{Role: "user", Content: "Write a Go function to reverse a string"},
	},
}, nil)
if err != nil {
	log.Fatal(err)
}
defer stream.Close()

for {
	chunk, err := stream.Read()
	if err == io.EOF {
		break
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(chunk)
}

// Code review
review, err := client.AI.Review(ctx, code, "go", []string{"security", "performance"}, nil)

// Check AI credits
credits, err := client.AI.GetCredits(ctx)
fmt.Printf("Remaining: %d credits\n", credits.Remaining)
```

## Error Handling

All API errors support Go's `errors.Is` for idiomatic sentinel error checking:

```go
result, err := client.DB.From("users").Select("*").Eq("id", "missing").Single(ctx)
if err != nil {
	log.Fatal(err) // Network or transport errors
}
if result.Error != nil {
	switch {
	case errors.Is(result.Error, vaif.ErrNotFound):
		fmt.Println("User not found")
	case errors.Is(result.Error, vaif.ErrUnauthorized):
		fmt.Println("Not authenticated — sign in first")
	case errors.Is(result.Error, vaif.ErrForbidden):
		fmt.Println("Insufficient permissions")
	case errors.Is(result.Error, vaif.ErrRateLimited):
		fmt.Println("Rate limited — try again later")
	case errors.Is(result.Error, vaif.ErrConflict):
		fmt.Println("Conflict — resource already exists")
	default:
		fmt.Printf("API error: %s (status %d)\n", result.Error.Message, result.Error.Status)
	}
}
```

### Sentinel Errors

| Error               | HTTP Status | Description             |
|----------------------|-------------|-------------------------|
| `ErrNotFound`        | 404         | Resource not found      |
| `ErrUnauthorized`    | 401         | Authentication required |
| `ErrForbidden`       | 403         | Insufficient permissions|
| `ErrRateLimited`     | 429         | Rate limit exceeded     |
| `ErrTimeout`         | 408         | Request timed out       |
| `ErrConflict`        | 409         | Resource conflict       |

### Error Types by Module

Each module has its own error type with additional context:

- `*vaif.ApiError` — Database errors (includes `Code`, `RequestID`)
- `*vaif.AuthError` — Auth errors (includes `Code`)
- `*vaif.StorageError` — Storage errors (includes `Path`)
- `*vaif.FunctionError` — Function errors (includes `Name`)
- `*vaif.AIError` — AI errors (includes `Code`)

All error types implement `Unwrap()` for sentinel matching with `errors.Is`.

## Thread Safety

`Client` and all sub-clients are safe for concurrent use from multiple goroutines. Create one client and share it across your application:

```go
var client = vaif.NewClient("project-id", "api-key")

func handler(w http.ResponseWriter, r *http.Request) {
	result, err := client.DB.From("users").Select("*").Execute(r.Context())
	// ...
}
```

## License

[MIT](LICENSE)

## Links

- [VAIF Studio Documentation](https://docs.vaif.studio)
- [GitHub Repository](https://github.com/vaifllc/vaif-go)
- [API Reference (pkg.go.dev)](https://pkg.go.dev/github.com/vaifllc/vaif-go)
- [Report an Issue](https://github.com/vaifllc/vaif-go/issues)
