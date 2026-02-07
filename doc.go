// Package vaif provides the official Go SDK for VAIF Studio, a backend-as-a-service
// platform. It provides type-safe access to database, auth, storage, serverless
// functions, realtime subscriptions, and AI code generation.
//
// # Quick Start
//
// Create a client and start making requests:
//
//	client := vaif.NewClient("your-project-id", "your-api-key")
//	defer client.Close()
//
//	ctx := context.Background()
//
//	// Query the database
//	result, err := client.DB.From("users").Select("*").Eq("active", true).Execute(ctx)
//
//	// Authenticate
//	resp, err := client.Auth.SignInWithPassword(ctx, vaif.SignInOptions{
//	    Email: "user@example.com", Password: "secret",
//	})
//
//	// Upload a file
//	f, _ := os.Open("photo.jpg")
//	upload, err := client.Storage.From("avatars").Upload(ctx, "photo.jpg", f, nil)
//
//	// Call an edge function
//	fn, err := client.Functions.Invoke(ctx, "send-email", map[string]any{"to": "a@b.com"}, nil)
//
//	// Generate schema with AI
//	schema, err := client.AI.GenerateSchema(ctx, vaif.SchemaGenerationRequest{
//	    Prompt: "Create a blog with posts and comments",
//	    Format: "sql",
//	}, nil)
//
// # Error Handling
//
// All errors support sentinel matching via errors.Is:
//
//	result, err := client.DB.From("users").Select("*").Single(ctx)
//	if errors.Is(err, vaif.ErrNotFound) {
//	    // handle missing resource
//	}
//	if errors.Is(err, vaif.ErrUnauthorized) {
//	    // handle auth failure
//	}
//
// # Configuration
//
// Use functional options to customize the client:
//
//	client := vaif.NewClient("project-id", "api-key",
//	    vaif.WithTimeout(10000),
//	    vaif.WithRetry(3, 500*time.Millisecond),
//	    vaif.WithDebug(true),
//	    vaif.WithHeaders(map[string]string{"X-Custom": "value"}),
//	)
//
// # Realtime Subscriptions
//
// Subscribe to database changes over WebSocket:
//
//	client.Realtime.Connect(nil)
//	ch := client.Realtime.Channel("room:lobby")
//	ch.OnPostgresChanges("INSERT", "public", "messages", func(payload any) {
//	    fmt.Println("New message:", payload)
//	})
//	ch.Subscribe(nil)
//
// # Thread Safety
//
// Client and all sub-clients are safe for concurrent use from multiple goroutines.
// Auth sessions are protected by sync.RWMutex, and realtime channels handle
// concurrent subscribe/unsubscribe operations safely.
package vaif
