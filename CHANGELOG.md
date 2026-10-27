# Changelog

All notable changes to this module are recorded in this file. The module
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## 1.0.0 (unreleased)

First release under the module path `github.com/vaifstudio/vaif-go`.

Requires Go 1.26.10 or later (the `go` directive in `go.mod`). The only
dependency is `github.com/gorilla/websocket` v1.5.3.

### Client

- `NewClient(projectID, apiKey, ...Option)` creates a client for one project.
  `NewPlatformClient(...Option)` creates a client for platform operations,
  authenticated with a console session passed through `WithSession`.
- Options: `WithBaseURL`, `WithTimeout`, `WithRetryPolicy`, `WithHTTPClient`,
  `WithHeader` and `WithSession`.
- A `Client` is safe for concurrent use. Every call takes a `context.Context`,
  and each HTTP attempt has its own timeout (30 seconds by default).
- The base URL must use HTTPS. Plain HTTP is accepted only for `localhost`,
  `127.0.0.1` and `::1`.
- `DefaultRetryPolicy` retries GET, HEAD and OPTIONS requests, and calls
  marked idempotent, on 429, 500, 502, 503 and 504 responses and on network
  errors: up to 3 retries, with exponential backoff from 1 second to
  30 seconds, jitter, and `Retry-After` honored on 429. A request of any
  method is retried when it failed before a connection was opened. `NoRetry`
  disables retries.

### Auth

- End-user accounts: `SignUp`, `SignIn`, `VerifyMFA`, `SignOut`, `GetUser`,
  `RequestPasswordReset` and `ResetPassword`.
- MFA management: `EnrollMFA`, `ConfirmMFA`, `DisableMFA` and
  `RegenerateMFACodes`.
- OAuth sign-in: `GetOAuthSignInURL` builds the provider URL with an S256
  PKCE challenge, and `ExchangeOAuthCode` completes the sign-in.
- Sessions are held in memory. Access tokens are refreshed 30 seconds before
  they expire and once after the API rejects one with a 401 response, and
  concurrent refreshes share a single request. After a 401, the request is
  sent again only with the session it carried, refreshed: if the user signed
  out or another session was set while it was in flight, the 401 is returned.
  A refresh that completes after `SignOut` or `SetSession` does not replace
  the newer state, and one that completes after `SignOut` revokes the refresh
  token the server issued to it.
- `SignOut` clears the session locally, then asks the server to revoke every
  session of the user. It sends the refresh token with the access token, so it
  works after the access token has expired.
- `OnSessionChange`, `SetSession`, `MarshalSession` and `UnmarshalSession`
  persist and restore a session.
- On a platform client, `Auth.SignUp` and `Auth.RequestPasswordReset` return
  an `*Error` with code `CONSOLE_ONLY` before any request is sent
  (`IsConsoleOnly`, `ErrConsoleOnly`). VAIF Studio accounts are created and
  reset in the console.

### Database

- `Client.From(table)` returns an immutable `Query`.
- Filters: `Eq`, `Neq`, `Gt`, `Gte`, `Lt`, `Lte`, `Like`, `ILike`, `In` and
  `Where`. Shaping: `Select`, `Order`, `Limit`, `Offset` and `Range`.
- Reads: `List`, `ListInto`, `First`, `Paginate`, `Get`, `GetInto` and
  `Count`. Writes: `Insert`, `InsertMany`, `Upsert`, `Update` and `Delete`.
- Invalid arguments are returned as `ErrInvalidArg` by the call that sends
  the request; the builder methods do not panic.

### Storage

- `Upload` and `Download` request a signed URL from the API and send or read
  the bytes directly at the object store. The API key, the session token and
  `WithHeader` headers are not sent to that URL.
- A signed URL that is not HTTPS (plain HTTP only for `localhost`,
  `127.0.0.1` and `::1`) is refused with code `INSECURE_STORAGE_URL` by
  `Upload`, `Download`, `CreateSignedURL` and `CreateSignedUploadURL`. The
  signed URL is not included in any error.
- `Download` reads an object into memory and refuses objects larger than
  64 MiB with `ErrBadResponse`.
- A redirect, such as the one the API's `GET /storage/download` route sends,
  keeps only the `Accept`, `Accept-Encoding`, `Accept-Language`,
  `Content-Type`, `Range` and `User-Agent` headers once it leaves the origin
  of the first request: never the API key, the session token, cookies or any
  other header the client or the caller set. A redirect to a URL that is not
  HTTPS is refused with code `INSECURE_REDIRECT`. A network failure after a
  redirect to another origin returns `ErrNetwork` with the message "could not
  reach the object store", and the error does not include the query or
  fragment of the URL that failed.
- `CreateSignedURL` and `CreateSignedUploadURL` return signed download and
  upload URLs; `List` and `Delete` list and remove objects and require
  platform authorization.

### Functions

- `Invoke` calls a deployed function with a method, path, query parameters
  and headers. An invocation is not retried after it may have reached the
  server unless `InvokeOptions.Idempotent` is set.
- A 401 the function returned (the response carries `X-Vaif-Run-Id`) is
  returned to the caller and does not refresh the session or run the function
  again. Only a 401 from the API, which refuses the access token before the
  function runs, refreshes the session and sends the invocation once more.

### Realtime

- `Realtime.Subscribe` streams a table's database changes over
  `/realtime/ws`, with server-side filters and column lists.
- Credentials are sent in the first frame, not in the URL. The socket uses
  `wss`; `ws` is accepted only for `localhost`, `127.0.0.1` and `::1`, and any
  other scheme is refused with code `INVALID_REALTIME_URL` before a token is
  read.
- Errors from `Subscribe` and `Next` are `*Error` values: a closed or failed
  connection matches `ErrNetwork`, and a silent connection or a missing
  acknowledgment matches `ErrTimeout`.
- Presence, broadcast and automatic reconnection are not included. After an
  error, call `Subscribe` again.

### VAIF Verament

- `Client.Verament(accountToken)` returns a `BlueprintClient` with `Chat`,
  `Replan`, `PlanWithDefaults`, `ConfirmPlan`, `QueueConfirmPlan`, `GetJob`,
  `CancelPlan` and `GetSession`.
- Engine tiers (`TierLite`, `TierStandard`, `TierMax`) and effort levels
  (`EffortQuick`, `EffortBalanced`, `EffortDeep`, `EffortExtended`).
  `BlueprintEngineOfferFrom` reads the offer returned when a requested engine
  is above the plan.

### Generated operations

The generated methods and types follow the API's OpenAPI description and
change when the API changes them; the changelog lists each such change.

- `Client.Operations` has 775 methods generated from the VAIF Studio OpenAPI
  description, with typed JSON responses, binary responses and server-sent
  event streams. A failed read of an event stream's body returns an `*Error`
  matching `ErrNetwork` or `ErrTimeout`. `OperationsTable` describes them, and
  `UngeneratedOperations` lists the 168 operations without a generated method
  and the reason for each.
- Staff-only operations are not generated, including the ones outside
  `/admin`. The 134 operations the API answers only for VAIF platform staff
  are listed as `platform staff only`: every operation under `/admin/*` and
  `/auth/impersonate/*`, plus feature flags (`/flags`), enterprise contract
  and invoice writes, `GET /ai/copilot/stats` and documentation edits under
  `/docs`.

### Errors

- Every API and network failure is an `*Error` carrying `Status`, `Code`,
  `Message`, `Details`, `RequestID`, `RetryAfter` and `Body`.
- Each `*Error` matches one sentinel through `errors.Is`: `ErrValidation`,
  `ErrAuth`, `ErrNotFound`, `ErrConflict`, `ErrRateLimited`, `ErrServer`,
  `ErrRequest`, `ErrNetwork`, `ErrTimeout` (which also matches `ErrNetwork`),
  `ErrInvalidArg`, `ErrBadResponse` or `ErrConsoleOnly`. A canceled context
  still matches `context.Canceled` or `context.DeadlineExceeded`.

### Earlier versions

Versions 0.1.0 and 0.1.1 were published under the module path
`github.com/vaifllc/vaif-go` and call API routes that are not served. Their
tags remain in this repository, and `go.mod` retracts them. Version 1.0.0
replaces them.
