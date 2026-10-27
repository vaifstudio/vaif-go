# VAIF Studio Go SDK

<!-- BEGIN GENERATED SDK STATUS: notice:go -->
> `github.com/vaifstudio/vaif-go` 1.0.0 is the version in this repository and has not been tagged yet. The hosted API (`https://api.vaif.studio`) and console are not available yet.
<!-- END GENERATED SDK STATUS: notice:go -->

The VAIF Studio Go SDK is the Go client for the [VAIF Studio](https://vaif.studio)
API: end-user authentication, database queries, file storage, functions,
realtime database changes, VAIF Verament and the rest of the API's operations.

`https://api.vaif.studio` is the client's default base URL. Until the hosted
API is available, requests made with the default base URL fail with an error
that matches `vaif.ErrNetwork`. The console (`https://console.vaif.studio`),
where projects and API keys are created, is not available yet either.

## Requirements

- Go 1.26.10 or later. The module's `go` directive is 1.26.10, the first
  Go 1.26 release with fixes for the standard-library vulnerabilities that
  [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) reports
  in earlier 1.26 releases. With the default `GOTOOLCHAIN=auto` setting, the
  go command (Go 1.21 or later) downloads Go 1.26.10 when the installed
  toolchain is older.
- One third-party dependency:
  [github.com/gorilla/websocket](https://github.com/gorilla/websocket) v1.5.3,
  used by realtime subscriptions.
- A VAIF Studio project ID and a project API key.

## Installation

Version 1.0.0 has not been tagged yet. Until the `v1.0.0` tag is published,
require the latest commit on the default branch:

```sh
go get github.com/vaifstudio/vaif-go@main
```

The go command records a pseudo-version for that commit in your `go.mod`.
Released versions are listed in [CHANGELOG.md](CHANGELOG.md).

Without a version, `go get` selects the repository's `v0.1.1` tag and fails
with a module path mismatch: the `v0.1.0` and `v0.1.1` tags predate this
module path, and this module's `go.mod` retracts them.

Import the package as `vaif`:

```go
import vaif "github.com/vaifstudio/vaif-go"
```

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	vaif "github.com/vaifstudio/vaif-go"
)

func main() {
	client, err := vaif.NewClient(os.Getenv("VAIF_PROJECT_ID"), os.Getenv("VAIF_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	signIn, err := client.Auth.SignIn(ctx, os.Getenv("VAIF_USER_EMAIL"), os.Getenv("VAIF_USER_PASSWORD"))
	if err != nil {
		log.Fatal(err)
	}
	if signIn.MFARequired {
		log.Fatal("this account uses MFA; finish the sign-in with client.Auth.VerifyMFA")
	}

	todos, err := client.From("todos").
		Eq("done", false).
		Order("created_at", true).
		Limit(20).
		List(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%d open todos\n", todos.Count)
	for _, row := range todos.Data {
		fmt.Println(row["title"])
	}
}
```

Apart from the standard proxy variables (`HTTPS_PROXY`, `HTTP_PROXY` and
`NO_PROXY`), which the default HTTP transport and the realtime dialer honor,
the SDK reads no environment variables; the program above reads its own.

## Authentication and configuration

### Project API keys

`NewClient` takes a project ID and a project API key. The key is sent with
every request in the `x-vaif-key` header.

- Publishable keys (`vk_pub_...`) are subject to row-level security. Use them
  in applications that run on end-user devices.
- Secret keys (`vk_srv_...`) bypass row-level security. Use them only on
  servers you control, and load them from a secret manager or the environment,
  not from source code.

### End-user sessions

After `SignIn`, `VerifyMFA`, `ExchangeOAuthCode`, or a `SignUp` that does not
wait for email verification, the client holds the user's session and sends its
access token as a bearer token. The access token is refreshed 30 seconds
before it expires. When the API rejects a request's access token with a 401
response, the client refreshes the session and sends the request once more.
If the user signed out, or another session was set, while the request was in
flight, the 401 is returned instead: a request is never sent again without a
session or as another user. Concurrent refreshes share one request.

Sessions are kept in memory. To keep a user signed in across restarts, save
the session when it changes and pass it back with `WithSession`:

```go
// The key must include the base URL and the project ID, so that clients for
// different projects, or for one project at different base URLs, never
// restore each other's session.
baseURL := "https://api.vaif.studio"
key := "vaif-session:" + baseURL + "|" + projectID

opts := []vaif.Option{vaif.WithBaseURL(baseURL)}
if raw := store.Get(key); raw != nil {
	saved, err := vaif.UnmarshalSession(raw)
	if err != nil || saved.AccessToken == "" {
		store.Delete(key)
	} else {
		opts = append(opts, vaif.WithSession(saved))
	}
}
client, err := vaif.NewClient(projectID, apiKey, opts...)
if err != nil {
	return err
}

client.Auth.OnSessionChange(func(event string, s *vaif.Session) {
	// event is vaif.EventSignedIn, vaif.EventTokenRefreshed or vaif.EventSignedOut.
	if s == nil {
		store.Delete(key)
		return
	}
	raw, err := vaif.MarshalSession(s)
	if err != nil || store.Put(key, raw) != nil {
		// Never leave the previous session restorable.
		store.Delete(key)
	}
})
```

`store` stands for your application's secure storage: `Get` returns nil when
nothing is saved under the key, `Put` returns an error when the value was not
saved, and `Delete` returns an error when the value was not deleted. A saved
entry that cannot be decoded or has no access token is deleted, not restored.
When a new session cannot be saved, the example deletes the saved one, so the
next start signs in again instead of restoring a session that was replaced. A
session contains a refresh token, so store it as a credential and never log
it. Listeners run synchronously on the goroutine that changed the session; do
not call `client.Auth` from a listener.

A listener cannot return an error to `SignOut`. After `SignOut` returns,
delete the saved session yourself and handle that error, so a failed delete
cannot leave the session restorable:

```go
// SignOut clears the session in memory even when its request fails.
signOutErr := client.Auth.SignOut(ctx)
if err := store.Delete(key); err != nil {
	return err
}
return signOutErr
```

### Client options

```go
client, err := vaif.NewClient(projectID, apiKey,
	vaif.WithTimeout(15*time.Second),
	vaif.WithRetryPolicy(vaif.DefaultRetryPolicy()),
	vaif.WithHTTPClient(&http.Client{Transport: transport}),
	vaif.WithHeader("X-App-Version", "1.4.2"),
)
```

| Option | Default | Notes |
| --- | --- | --- |
| `WithBaseURL` | `https://api.vaif.studio` | Must use HTTPS. Plain HTTP is accepted only for `localhost`, `127.0.0.1` and `::1`. |
| `WithTimeout` | 30 seconds | Applies to each attempt, except the VAIF Verament calls that wait for a plan or a build (see [VAIF Verament](#vaif-verament)) and an `Invoke` with `InvokeOptions.Timeout`. A deadline on the call's context also applies. |
| `WithRetryPolicy` | `DefaultRetryPolicy()` | `NoRetry()` disables retries. |
| `WithHTTPClient` | a new `http.Client` | Used for every HTTP request, including uploads to presigned URLs; set a proxy or TLS configuration here. Realtime connections use their own dialer, which honors the standard proxy environment variables. |
| `WithHeader` | none | Adds a header to every API request. |
| `WithSession` | none | Restores a saved end-user session. The client keeps its own copy. An empty access token is refused with `ErrInvalidArg`, and a zero `ExpiresAt` is treated as one hour from now. |

A `Client` is safe for concurrent use. Create one per project and reuse it.

### Retries

`DefaultRetryPolicy` retries a GET, HEAD or OPTIONS request, or a call marked
idempotent, after a 429, 500, 502, 503 or 504 response or a network error. It
makes up to 3 retries, waiting 1 second and doubling up to 30 seconds, with up
to 25% jitter. On a 429 response, a `Retry-After` header sets the wait
instead, up to the same 30-second cap. A request with any other method is
retried only when it failed
before a connection was opened, so a write is not replayed after it may have
reached the server.

### Platform client

`NewPlatformClient` creates a client without a project, for VAIF Studio
platform operations authenticated with a console session passed through
`WithSession`. VAIF Studio accounts are created and reset in the console only.
On a platform client, `Auth.SignUp` and `Auth.RequestPasswordReset`, and the
generated `PostAuthSignup` and `PostAuthForgotPassword` on any client, return
an error with code `CONSOLE_ONLY` before sending a request:

```go
if details, ok := vaif.IsConsoleOnly(err); ok {
	fmt.Println("Continue in the console:", details.ConsoleURL)
}
```

## Usage

Each example assumes a `client` from `NewClient` and a `ctx` of type
`context.Context`.

### Auth

```go
// Create an account. When the project requires email verification, no
// session is returned until the user verifies.
signUp, err := client.Auth.SignUp(ctx, "ada@example.com", password, &vaif.SignUpOptions{Name: "Ada"})
if err != nil {
	return err
}
if signUp.PendingVerification {
	fmt.Println(signUp.Message)
}

// Sign in. When the account has MFA enabled, finish with a TOTP or backup code.
signIn, err := client.Auth.SignIn(ctx, "ada@example.com", password)
if err != nil {
	return err
}
if signIn.MFARequired {
	if _, err := client.Auth.VerifyMFA(ctx, signIn.ChallengeToken, code); err != nil {
		return err
	}
}

user, err := client.Auth.GetUser(ctx)
if err != nil {
	return err
}
fmt.Println(user.ID)

// Revoke the session on the server and clear it locally.
if err := client.Auth.SignOut(ctx); err != nil {
	return err
}
```

`SignOut` clears the local session, then asks the server to revoke it; the
server signs the user out of every session. The request carries the refresh
token as well as the access token, so it works after the access token has
expired. The local session is cleared even when the request fails, and a 401
from the server is not reported as an error. A refresh that finishes after
`SignOut` returns an error with code `NOT_SIGNED_IN`, does not restore the
session, and revokes the refresh token the server issued to it.

`RequestPasswordReset` sends the reset email, and `ResetPassword` completes
the reset with the token from that email.

Multi-factor authentication:

```go
enrollment, err := client.Auth.EnrollMFA(ctx)
if err != nil {
	return err
}
// Show enrollment.OTPAuthURI as a QR code, then confirm with a code from the
// user's authenticator app.
backup, err := client.Auth.ConfirmMFA(ctx, enrollment.EnrollToken, code)
if err != nil {
	return err
}
// Show backup.BackupCodes to the user once. Do not log or store them.
```

`DisableMFA` and `RegenerateMFACodes` take a current code.

OAuth sign-in uses an S256 PKCE challenge:

```go
start, err := client.Auth.GetOAuthSignInURL(ctx, vaif.OAuthOptions{
	Provider:    "google",
	RedirectURL: "https://app.example.com/auth/callback",
})
if err != nil {
	return err
}
// Keep start.State and start.CodeVerifier on your server and send the user
// to start.URL. On the callback, check that its state equals start.State,
// then exchange the one-time code:
result, err := client.Auth.ExchangeOAuthCode(ctx, callbackCode, start.CodeVerifier)
if err != nil {
	return err
}
if result.MFARequired {
	if _, err := client.Auth.VerifyMFA(ctx, result.ChallengeToken, code); err != nil {
		return err
	}
}
```

### Database queries

`From` starts a query on a table. A `Query` is immutable: every method returns
a new `Query`, so a base query can be shared and extended.

```go
todos := client.From("todos")

open, err := todos.
	Select("id", "title", "priority").
	Eq("owner_id", userID).
	In("status", "open", "blocked").
	Gte("priority", 2).
	Order("priority", true).
	Limit(50).
	List(ctx)
if err != nil {
	return err
}
// open.Data is a []vaif.Row; open.Count is the total number of matching rows.
fmt.Println(open.Count, len(open.Data))

type Todo struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Done  bool   `json:"done"`
}
var items []Todo
total, err := todos.ILike("title", "%milk%").ListInto(ctx, &items)
if err != nil {
	return err
}
fmt.Println(total, len(items))

page, err := todos.Eq("done", false).Paginate(ctx, 2, 20)
if err != nil {
	return err
}
fmt.Println(page.Total, page.HasNext())
```

Writes return the stored row:

```go
created, err := todos.Insert(ctx, vaif.Row{"title": "Buy milk"})
if err != nil {
	return err
}
fmt.Println(created["id"])

if _, err := todos.Update(ctx, "row-id", vaif.Row{"done": true}); err != nil {
	return err
}
if _, err := todos.Upsert(ctx, vaif.Row{"email": "ada@example.com", "name": "Ada"}, "email"); err != nil {
	return err
}
if err := todos.Delete(ctx, "row-id"); err != nil {
	return err
}
```

Filters (`Eq`, `Neq`, `Gt`, `Gte`, `Lt`, `Lte`, `Like`, `ILike`, `In`) are
combined with AND. `Where` adds a raw clause, such as an OR group:

```go
urgent := todos.Where(map[string]any{"OR": []any{
	map[string]any{"field": "status", "value": "open"},
	map[string]any{"field": "priority", "operator": "gte", "value": 3},
}})
```

`Limit` accepts 1 to 1000 (`vaif.MaxLimit`). `Get` and `GetInto` read one row
by primary key and return an error matching `vaif.ErrNotFound` when it does
not exist. `First` returns the first match or nil, and `Count` returns the
number of matching rows. Invalid arguments, such as a column name that is not
an SQL identifier, are returned by the call that sends the request; the
builder methods do not panic.

### Storage

```go
_, err := client.Storage.Upload(ctx, "avatars/u1.png", pngBytes, &vaif.UploadOptions{ContentType: "image/png"})
if err != nil {
	return err
}

data, err := client.Storage.Download(ctx, "avatars/u1.png", nil)
if err != nil {
	return err
}
fmt.Println(len(data))

downloadURL, err := client.Storage.CreateSignedURL(ctx, "avatars/u1.png", nil)
if err != nil {
	return err
}
fmt.Println(downloadURL)
```

`Upload` and `Download` ask the API for a signed URL and send or read the
bytes directly at the object store. The API key, the session token and
`WithHeader` headers are not sent to that URL. Without a `Bucket` option, the
project's default bucket is used.

- Every signed URL the API issues must use HTTPS; plain HTTP is accepted only
  for `localhost`, `127.0.0.1` and `::1`. Any other URL is refused with code
  `INSECURE_STORAGE_URL` before a request is sent to it.
- `Download` reads the object into memory and refuses objects larger than
  64 MiB with `vaif.ErrBadResponse`. For larger objects, download from a
  `CreateSignedURL` URL with your own HTTP client.
- A failed request to the object store is returned with code
  `STORAGE_UPLOAD_FAILED` or `STORAGE_DOWNLOAD_FAILED` when the response
  carries no code of its own. The signed URL is not included in the error.
- `CreateSignedURL` returns a download URL that the API issues for
  10 minutes.
- `CreateSignedUploadURL(ctx, key, sizeBytes, opts)` returns a URL that
  accepts one HTTP PUT of exactly `sizeBytes` bytes (1 byte to 5 GiB) and, when
  `ContentType` is set, exactly that content type.
- `List` and `Delete` call routes that require platform authorization.

The client follows HTTP redirects, such as the one the API's
`GET /storage/download` route answers with. Once a redirect leaves the origin
of the first request, it carries only the `Accept`, `Accept-Encoding`,
`Accept-Language`, `Content-Type`, `Range` and `User-Agent` headers: never the
API key, the session token, cookies, `WithHeader` headers or headers passed
for one call. A redirect to a URL that is not HTTPS (plain HTTP only for
`localhost`, `127.0.0.1` and `::1`) is refused with code `INSECURE_REDIRECT`.
A network failure after a redirect to another origin returns
`vaif.ErrNetwork` with the message "could not reach the object store", and
the error does not include the query or fragment of the URL that failed.

### Functions

```go
resp, err := client.Functions.Invoke(ctx, "send-email", map[string]any{"to": "ada@example.com"}, nil)
if err != nil {
	return err
}
var out struct {
	Sent bool `json:"sent"`
}
if err := resp.JSON(&out); err != nil {
	return err
}

report, err := client.Functions.Invoke(ctx, "report", nil, &vaif.InvokeOptions{
	Method: "GET",
	Path:   "/daily",
	Query:  map[string]string{"day": "mon"},
})
if err != nil {
	return err
}
fmt.Println(report.Status, report.Text())
```

A non-2xx response from the function is returned as a `*vaif.Error` whose
`Body` holds the function's response. An invocation is not retried after it
may have reached the server unless `InvokeOptions.Idempotent` is set.

The API marks every response a function produced with an `X-Vaif-Run-Id`
header. A 401 that carries it is the function's own answer: it is returned as
an error matching `vaif.ErrAuth`, and the client neither refreshes the session
nor runs the function again. A 401 without it means the API refused the access
token before the function ran; the client refreshes the session and sends the
invocation once more.

### Realtime

```go
sub, err := client.Realtime.Subscribe(ctx, "todos", &vaif.SubscribeOptions{
	Filters: []vaif.RealtimeFilter{{Field: "owner_id", Value: userID}},
	Columns: []string{"id", "title", "done"},
})
if err != nil {
	return err
}
defer sub.Close()

for {
	event, err := sub.Next(ctx)
	if err != nil {
		return err // Subscribe again to resume.
	}
	fmt.Println(event.Op, event.Record, event.OldRecord)
}
```

- `Subscribe` returns after the server acknowledges the subscription. The
  context passed to it controls the connection's lifetime.
- A filter's `Operator` is one of `eq` (the default), `neq`, `gt`, `lt`,
  `gte`, `lte`, `in`, `like` or `ilike`.
- Credentials are sent in the first frame, not in the URL. The connection
  uses `wss`; `ws` is used only for `localhost`, `127.0.0.1` and `::1`, and any
  other base URL is refused with code `INVALID_REALTIME_URL`.
- The client sends heartbeats, and `Next` treats a connection that stays
  silent for three heartbeat intervals as closed.
- Errors from `Subscribe` and `Next` are `*vaif.Error` values. A closed or
  failed connection matches `vaif.ErrNetwork`, a silent one or a subscription
  that is not acknowledged within the client's timeout matches
  `vaif.ErrTimeout`, and a refusal from the server carries the server's code.
- Call `Next` from one goroutine at a time. After any error, including an
  expired access token, call `Subscribe` again: there is no automatic
  reconnection, and the access token is not refreshed during an open
  subscription. Presence and broadcast channels are not included.

### VAIF Verament

VAIF Verament answers a build request with a plan. Planning is charged; the
build is not run, or charged, until you confirm the plan. Verament acts for a
VAIF Studio account, so it takes that account's access token.

```go
verament := client.Verament(accountToken)

planned, err := verament.Chat(ctx, vaif.BlueprintChatInput{
	ProjectID: projectID,
	Message:   "tables for a todo list",
	Tier:      vaif.TierStandard,
	Effort:    vaif.EffortBalanced,
})
if err != nil {
	return err
}
if planned.PreflightRequired == nil || planned.PreflightRequired.Plan == nil {
	fmt.Println(planned.Response.Content) // A reply without a plan.
	return nil
}
card := planned.PreflightRequired.Plan

built, err := verament.ConfirmPlan(ctx, vaif.ConfirmPlanInput{PlanRef: vaif.PlanRef{
	ProjectID: projectID,
	SessionID: planned.SessionID,
	PlanID:    card.PlanID,
	Message:   "tables for a todo list",
}})
if err != nil {
	return err
}
for _, file := range built.Files() {
	fmt.Println(file.Path)
}
```

- A plan can be confirmed once, within 30 minutes, in its session, with the
  message it was made for. A refused confirm is a `*vaif.Error` with the
  API's code: 404 matches `vaif.ErrNotFound`, 409 matches `vaif.ErrConflict`
  (for example `PLAN_STALE`), and 410 means the plan expired.
- When the account's balance is below the run's maximum, `ConfirmPlan`
  returns `PreflightRequired` with a credit choice; confirm again with
  `CreditChoice` set to `"smaller"` or `"until_zero"`.
- `QueueConfirmPlan` starts the build in the background; poll `GetJob` for
  its result. `CancelPlan` drops a plan, and `GetSession` returns the plan a
  session is waiting on.
- `AutoConfirm` on `BlueprintChatInput` builds in the same call when the
  whole run costs at most `MaxCredits`.
- `Chat`, `Replan` and `PlanWithDefaults` wait up to 2 minutes for the API's
  answer, and `ConfirmPlan` and `Chat` with `AutoConfirm` wait up to
  15 minutes (`vaif.BuildTimeout`), whatever `WithTimeout` is set to. A
  deadline on the context still applies.
- Engines are `TierLite`, `TierStandard` and `TierMax` (Verament Lite 1.0,
  Standard 1.0 and Max 1.0), and effort levels are `EffortQuick`,
  `EffortBalanced`, `EffortDeep` and `EffortExtended`. Left empty, the plan's
  defaults apply. Requesting an engine above the plan returns a 403 before
  any model call; `vaif.BlueprintEngineOfferFrom(err)` reads the offer it
  carries.

### Generated API operations

`client.Operations` has 801 methods generated from the OpenAPI description of
the VAIF Studio API, one for each operation except the 142 that
`vaif.UngeneratedOperations()` lists. They use the same transport, session
handling, retries and errors as the rest of the client.

```go
project, err := client.Operations.GetProjectsByProjectId(ctx, vaif.OperationOptions{
	Path: map[string]string{"projectId": client.ProjectID()},
})
if err != nil {
	return err
}
fmt.Println(project)
```

- `OperationOptions` takes `Path`, `Query` (`url.Values`), `Body` and
  `Headers`. Required path, query and body arguments are checked before the
  request is sent; the API validates the rest.
- JSON responses are returned as Go types; open objects and mixed unions are
  `any`. Binary responses are returned as `[]byte`. Server-sent event streams
  are returned as a live `*http.Response`: close its body when done; the
  context passed to the call bounds its lifetime. A failed read of the body
  returns a `*vaif.Error` matching `vaif.ErrNetwork` (or `vaif.ErrTimeout`),
  and the end of the stream returns `io.EOF`.
- `vaif.OperationsTable()` describes every generated method, and
  `vaif.UngeneratedOperations()` gives the reason each of the 142 other
  operations has no method. 108 of them are the operations VAIF's own staff
  tools use (`/admin/*` and `/auth/impersonate/*`), listed with the reason
  `platform staff only`.
- Each operation requires the credentials the API requires for it: a project
  key, an end-user session, or a console session on a platform client.

## Error handling

Every failure that comes from the API or the network is a `*vaif.Error`. Match
its kind with `errors.Is` and read its fields with `errors.As`:

```go
_, err := client.From("todos").Get(ctx, "row-id")

var apiErr *vaif.Error
switch {
case err == nil:
case errors.Is(err, vaif.ErrNotFound):
	fmt.Println("no such row")
case errors.Is(err, vaif.ErrRateLimited):
	if errors.As(err, &apiErr) {
		fmt.Println("retry after", apiErr.RetryAfter)
	}
case errors.As(err, &apiErr):
	log.Printf("status=%d code=%s request_id=%s: %s", apiErr.Status, apiErr.Code, apiErr.RequestID, apiErr.Message)
default:
	log.Print(err)
}
```

| Sentinel | Cause |
| --- | --- |
| `ErrValidation` | 400 or 422 |
| `ErrAuth` | 401 or 403 |
| `ErrNotFound` | 404 |
| `ErrConflict` | 409 |
| `ErrRateLimited` | 429; `RetryAfter` holds the server's hint |
| `ErrServer` | 5xx |
| `ErrRequest` | any other non-2xx status |
| `ErrNetwork` | no usable response |
| `ErrTimeout` | a timeout; also matches `ErrNetwork` |
| `ErrInvalidArg` | an argument refused before the request was sent |
| `ErrBadResponse` | a 2xx response that could not be decoded |
| `ErrConsoleOnly` | a platform sign-up or password reset (code `CONSOLE_ONLY`) |

`*vaif.Error` has these fields: `Kind` (the sentinel), `Status` (0 when no
response was received), `Code` (the API's machine-readable code), `Message`,
`Details`, `RequestID`, `RetryAfter`, `Body` (the decoded response body) and
`Err` (the underlying cause of a network error). Include `RequestID` when you
contact support about a failed request.

The client itself sets these codes:

| Code | Kind | Meaning |
| --- | --- | --- |
| `NOT_SIGNED_IN` | `ErrAuth` | The call needs an end-user session and there is none, or the user signed out while a refresh was in flight. |
| `NO_REFRESH_TOKEN` | `ErrAuth` | The session has no refresh token to refresh with. |
| `CONSOLE_ONLY` | `ErrConsoleOnly` | Platform sign-up and password reset happen in the console. |
| `PROJECT_ID_REQUIRED` | `ErrInvalidArg` | OAuth sign-in needs a project client. |
| `INVALID_REALTIME_URL` | `ErrInvalidArg` | The base URL cannot carry a realtime connection securely. |
| `BUCKET_NOT_FOUND` | `ErrNotFound` | No bucket has the given name. |
| `STORAGE_UPLOAD_FAILED` | by status, or `ErrNetwork` | The `Upload` request to the object store failed, and the failure carried no code of its own. |
| `STORAGE_DOWNLOAD_FAILED` | by status, or `ErrNetwork` | The `Download` request to the object store failed, and the failure carried no code of its own. |
| `INSECURE_STORAGE_URL` | `ErrBadResponse` | The API returned a signed storage URL that is not HTTPS (plain HTTP is accepted only for `localhost`, `127.0.0.1` and `::1`). |
| `INSECURE_REDIRECT` | `ErrRequest` | A response redirected to a URL that is not HTTPS (plain HTTP is accepted only for `localhost`, `127.0.0.1` and `::1`). |

When the call's context is canceled or its deadline passes, the error still
matches `context.Canceled` or `context.DeadlineExceeded`. Match on kinds and
codes rather than on `Message` text, which can change in any release.

## Versioning

This module follows [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html).
Within major version 1, minor releases add functionality and patch releases
fix defects, without breaking code that compiles and works against an earlier
1.x release.

These are breaking changes, made only in a new major version, which in Go
also changes the module path (for example, `github.com/vaifstudio/vaif-go/v2`):

- removing or renaming an exported identifier, or changing the signature of an
  exported function or method;
- removing an exported struct field or changing its type;
- changing which sentinel an error matches, or removing a code the client sets
  itself.

These are not breaking changes:

- adding exported functions, methods, types, struct fields, options, sentinel
  errors or error codes;
- adding generated operation methods when the API adds operations;
- changing `Message` text or default values such as the timeout and the
  retry policy (set them explicitly to keep them fixed);
- raising the minimum Go version, for example to require a Go release with
  security fixes. The changelog records each change of minimum version.

Use keyed fields in struct literals of SDK types, so that a field added in a
minor release does not stop your code from compiling.

The generated operation methods in `client.Operations` and their request and
response types follow the API's OpenAPI description, and the rules above do
not cover them. When the API removes an operation or changes a field of its
request or response, the matching generated method or type changes in the
next release, and the changelog lists the change. If you call generated
operations, read the changelog before you upgrade.

## Support

- Bugs and feature requests: open an issue in this repository.
- Account and billing questions: support@vaif.studio.

## Security

Report vulnerabilities privately to security@vaif.studio, not in a public
issue. See [SECURITY.md](SECURITY.md) for what to include and what happens
next.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). This repository is a published copy of
the SDK; pull requests are reviewed and, when accepted, applied upstream by the
maintainers.

## License

MIT. See [LICENSE](LICENSE).
