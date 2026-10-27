// Package vaif is the Go client for the VAIF Studio API.
//
// It covers a project's end-user auth, table queries, file storage,
// functions, realtime database changes, VAIF Verament and the generated API
// operations. Every call takes a context.Context, applies a per-attempt
// timeout, retries transient failures with bounded exponential backoff, and
// returns *Error values that match sentinel errors through errors.Is:
//
//	client, err := vaif.NewClient("YOUR_PROJECT_ID", "vk_pub_...")
//	if err != nil {
//		log.Fatal(err)
//	}
//	if _, err := client.Auth.SignIn(ctx, "ada@example.com", "password"); err != nil {
//		log.Fatal(err)
//	}
//	todos, err := client.From("todos").Eq("done", false).Order("created_at", true).Limit(20).List(ctx)
//	if err != nil {
//		log.Fatal(err)
//	}
//	fmt.Println(todos.Count)
//
// Only replay-safe requests (GET, HEAD, OPTIONS, and calls marked idempotent)
// are retried, on 429, 500, 502, 503 and 504 responses and on network errors;
// a request that failed before a connection was opened is retried for any
// method. Access tokens are refreshed 30 seconds before they expire and once
// after the API rejects one with a 401 response, and concurrent refreshes
// share one request.
//
// Realtime streams a table's database changes. There is no automatic
// reconnection: after an error, call Subscribe again. Operations exposes the
// operations generated from the API's OpenAPI description.
package vaif
