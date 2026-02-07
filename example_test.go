package vaif_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	vaif "github.com/vaifllc/vaif-go"
)

func Example() {
	// Create client with retry and debug enabled
	client := vaif.NewClient(
		"your-project-id",
		"your-api-key",
		vaif.WithDebug(true),
		vaif.WithRetry(3, 500*time.Millisecond),
		vaif.WithTimeout(15000),
	)
	defer client.Close()

	ctx := context.Background()

	// --- Auth: Sign in ---
	resp, err := client.Auth.SignInWithPassword(ctx, vaif.SignInOptions{
		Email:    "user@example.com",
		Password: "password123",
	})
	if err != nil {
		log.Fatal(err)
	}
	if resp.Error != nil {
		log.Fatal(resp.Error.Message)
	}
	fmt.Printf("Signed in as: %s\n", resp.User.Email)
}

func ExampleClient_database() {
	client := vaif.NewClient("project-id", "api-key")
	defer client.Close()
	ctx := context.Background()

	// Select all users
	users, err := client.DB.From("users").Select("*").Execute(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Found %d users\n", len(users.Data))

	// Insert a row
	_, err = client.DB.From("posts").Insert(map[string]any{
		"title":   "Hello World",
		"content": "My first post",
	}).Execute(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// Query with filters, ordering, and pagination
	result, err := client.DB.From("posts").
		Select("id, title, created_at").
		Eq("published", true).
		Order("created_at", false).
		Limit(10).
		Execute(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Found %d posts\n", len(result.Data))

	// Get a single row
	single, err := client.DB.From("users").
		Select("*").
		Eq("id", "user-123").
		Single(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if single.Error != nil {
		if errors.Is(single.Error, vaif.ErrNotFound) {
			fmt.Println("User not found")
		}
	} else {
		fmt.Printf("User: %v\n", single.Data["email"])
	}
}

func ExampleQueryBuilder_upsert() {
	client := vaif.NewClient("project-id", "api-key")
	ctx := context.Background()

	// Upsert: insert or update on email conflict
	_, err := client.DB.From("users").
		Upsert(map[string]any{
			"email": "john@example.com",
			"name":  "John Updated",
		}, "email").
		Returning("id, email, name").
		Execute(ctx)
	if err != nil {
		log.Fatal(err)
	}
}

func ExampleQueryBuilder_executeTo() {
	client := vaif.NewClient("project-id", "api-key")
	ctx := context.Background()

	// Typed result scanning with ExecuteInto
	type User struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}

	var users []User
	err := client.DB.From("users").Select("id, email, name").ExecuteInto(ctx, &users)
	if err != nil {
		log.Fatal(err)
	}
	for _, u := range users {
		fmt.Printf("%s: %s\n", u.Name, u.Email)
	}
}

func ExampleDatabase_raw() {
	client := vaif.NewClient("project-id", "api-key")
	ctx := context.Background()

	// Raw parameterized SQL
	result, err := client.DB.Raw(ctx,
		"SELECT * FROM users WHERE age > $1 AND status = $2",
		18, "active",
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Found %d users\n", len(result.Data))
}

func ExampleAuth_signUp() {
	client := vaif.NewClient("project-id", "api-key")
	ctx := context.Background()

	// Sign up a new user
	resp, err := client.Auth.SignUp(ctx, vaif.SignUpOptions{
		Email:    "newuser@example.com",
		Password: "securepassword123",
		Data:     map[string]any{"display_name": "New User"},
	})
	if err != nil {
		log.Fatal(err)
	}
	if resp.Error != nil {
		log.Fatal(resp.Error.Message)
	}
	fmt.Printf("User created: %s\n", resp.User.ID)
}

func ExampleAuth_onAuthStateChange() {
	client := vaif.NewClient("project-id", "api-key")

	// Listen for auth state changes
	unsub := client.Auth.OnAuthStateChange(func(event vaif.AuthChangeEvent, session *vaif.Session) {
		switch event {
		case vaif.AuthEventSignedIn:
			fmt.Println("User signed in")
		case vaif.AuthEventSignedOut:
			fmt.Println("User signed out")
		case vaif.AuthEventTokenRefreshed:
			fmt.Println("Token refreshed")
		}
	})

	// Later: stop listening
	_ = unsub
}

func ExampleAuth_oAuth() {
	client := vaif.NewClient("project-id", "api-key")

	// Get OAuth URL for Google sign-in
	url, err := client.Auth.SignInWithOAuth(vaif.OAuthGoogle, "https://myapp.com/callback", nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Redirect user to: %s\n", url)
}

func ExampleStorage_upload() {
	client := vaif.NewClient("project-id", "api-key")
	ctx := context.Background()

	// Upload a file
	data := strings.NewReader("Hello, World!")
	result, err := client.Storage.From("documents").Upload(ctx, "hello.txt", data, &vaif.UploadOptions{
		ContentType: "text/plain",
		Upsert:      true,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Uploaded: %s\n", result.Path)

	// Get public URL
	url := client.Storage.From("documents").GetPublicURL("hello.txt")
	fmt.Printf("Public URL: %s\n", url)
}

func ExampleStorage_downloadToFile() {
	client := vaif.NewClient("project-id", "api-key")
	ctx := context.Background()

	// Download directly to a local file
	err := client.Storage.From("backups").DownloadToFile(ctx, "db-backup.sql", "/tmp/backup.sql", nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Downloaded to /tmp/backup.sql")
}

func ExampleStorage_signedURL() {
	client := vaif.NewClient("project-id", "api-key")
	ctx := context.Background()

	// Create a signed URL that expires in 1 hour
	signedURL, err := client.Storage.From("private").CreateSignedURL(ctx, "report.pdf", vaif.SignedURLOptions{
		ExpiresIn: 3600,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Signed URL: %s\n", signedURL)
}

func ExampleFunctions_invoke() {
	client := vaif.NewClient("project-id", "api-key")
	ctx := context.Background()

	// Invoke an edge function
	result, err := client.Functions.Invoke(ctx, "send-email", map[string]any{
		"to":      "user@example.com",
		"subject": "Welcome!",
		"body":    "Thanks for signing up.",
	}, nil)
	if err != nil {
		log.Fatal(err)
	}
	if result.Error != nil {
		log.Fatal(result.Error)
	}
	fmt.Printf("Function result: %v\n", result.Data)
}

func ExampleAI_generateSchema() {
	client := vaif.NewClient("project-id", "api-key")
	ctx := context.Background()

	// Generate a database schema with AI
	schema, err := client.AI.GenerateSchema(ctx, vaif.SchemaGenerationRequest{
		Prompt:           "Create a blog with posts, authors, and comments",
		Format:           "sql",
		IncludeRelations: true,
		IncludeIndexes:   true,
	}, nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Generated %d tables using %d credits\n", len(schema.Tables), schema.CreditsUsed)
	fmt.Println(schema.Schema)
}

func ExampleAI_chatStream() {
	client := vaif.NewClient("project-id", "api-key")
	ctx := context.Background()

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
	fmt.Println()
}

func ExampleRealtime_subscribe() {
	client := vaif.NewClient("project-id", "api-key")

	// Connect with callbacks
	err := client.Realtime.Connect(&vaif.RealtimeOptions{
		Heartbeat:            true,
		HeartbeatInterval:    30000,
		AutoReconnect:        true,
		MaxReconnectAttempts: 5,
		OnConnect: func(state vaif.ConnectionState) {
			fmt.Println("Connected to realtime")
		},
		OnDisconnect: func(state vaif.ConnectionState) {
			fmt.Println("Disconnected from realtime")
		},
		OnError: func(err error) {
			fmt.Printf("Realtime error: %v\n", err)
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Realtime.Disconnect()

	// Subscribe to database changes
	ch := client.Realtime.Channel("room:lobby")

	ch.OnPostgresChanges("INSERT", "public", "messages", func(payload any) {
		fmt.Printf("New message: %v\n", payload)
	})

	ch.OnPostgresChanges("UPDATE", "public", "messages", func(payload any) {
		fmt.Printf("Message updated: %v\n", payload)
	})

	ch.Subscribe(func(status vaif.ChannelStatus, err error) {
		if err != nil {
			log.Printf("Subscribe error: %v", err)
			return
		}
		fmt.Printf("Channel status: %s\n", status)
	})

	// Broadcast to other clients
	ch.Broadcast("typing", map[string]any{
		"user": "john",
	})

	// Track presence
	ch.Track(map[string]any{
		"user":      "john",
		"online_at": time.Now().Unix(),
	})
}

func ExampleClient_errorHandling() {
	client := vaif.NewClient("project-id", "api-key")
	ctx := context.Background()

	// All errors support sentinel matching
	_, err := client.DB.From("users").Select("*").Single(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// Check for specific error types
	result, err := client.DB.From("nonexistent").Select("*").Single(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if result.Error != nil {
		if errors.Is(result.Error, vaif.ErrNotFound) {
			fmt.Println("Resource not found")
		} else if errors.Is(result.Error, vaif.ErrUnauthorized) {
			fmt.Println("Not authenticated")
		} else if errors.Is(result.Error, vaif.ErrRateLimited) {
			fmt.Println("Rate limited, try again later")
		}
	}
}

func ExampleNewClient_withRetry() {
	// Create a client with automatic retry on transient errors
	client := vaif.NewClient(
		"project-id",
		"api-key",
		vaif.WithRetry(3, 500*time.Millisecond),
		vaif.WithMaxRetryDelay(10*time.Second),
	)
	defer client.Close()

	// All requests will automatically retry on 5xx, 429, and network errors
	_ = client
}
