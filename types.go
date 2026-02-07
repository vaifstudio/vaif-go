package vaif

import (
	"errors"
	"fmt"
	"time"
)

// Sentinel errors for common API failure conditions. Use errors.Is to check:
//
//	if errors.Is(err, vaif.ErrNotFound) { ... }
var (
	// ErrNotFound indicates the requested resource was not found (HTTP 404).
	ErrNotFound = errors.New("vaif: not found")
	// ErrUnauthorized indicates invalid or missing authentication (HTTP 401).
	ErrUnauthorized = errors.New("vaif: unauthorized")
	// ErrForbidden indicates insufficient permissions (HTTP 403).
	ErrForbidden = errors.New("vaif: forbidden")
	// ErrRateLimited indicates the request was rate-limited (HTTP 429).
	ErrRateLimited = errors.New("vaif: rate limited")
	// ErrTimeout indicates the request timed out.
	ErrTimeout = errors.New("vaif: timeout")
	// ErrConflict indicates a conflict, such as a unique constraint violation (HTTP 409).
	ErrConflict = errors.New("vaif: conflict")
)

// VaifConfig holds client configuration.
type VaifConfig struct {
	ProjectID        string            `json:"projectId"`
	APIKey           string            `json:"apiKey"`
	APIURL           string            `json:"apiUrl"`
	RealtimeURL      string            `json:"realtimeUrl"`
	Timeout          int               `json:"timeout"` // milliseconds
	Headers          map[string]string `json:"headers"`
	Debug            bool              `json:"debug"`
	AutoRefreshToken bool              `json:"autoRefreshToken"`
	PersistSession   bool              `json:"persistSession"`

	// Retry configuration
	MaxRetries     int           `json:"maxRetries"`
	RetryBackoff   time.Duration `json:"retryBackoff"`
	MaxRetryDelay  time.Duration `json:"maxRetryDelay"`
}

// ApiError represents an API error response. It implements the error interface
// and supports errors.Is / errors.As for sentinel error matching.
type ApiError struct {
	Message   string `json:"message"`
	Code      string `json:"code,omitempty"`
	Status    int    `json:"status,omitempty"`
	Details   any    `json:"details,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	Operation string `json:"operation,omitempty"` // which API call failed
	sentinel  error  // sentinel error for Is/As matching
}

// Error returns the error message.
func (e *ApiError) Error() string {
	if e.Operation != "" {
		return fmt.Sprintf("%s: %s (status %d)", e.Operation, e.Message, e.Status)
	}
	return e.Message
}

// Unwrap returns the sentinel error for use with errors.Is.
func (e *ApiError) Unwrap() error {
	return e.sentinel
}

// sentinelForStatus maps HTTP status codes to sentinel errors.
func sentinelForStatus(status int) error {
	switch status {
	case 401:
		return ErrUnauthorized
	case 403:
		return ErrForbidden
	case 404:
		return ErrNotFound
	case 409:
		return ErrConflict
	case 429:
		return ErrRateLimited
	default:
		return nil
	}
}

// ApiResponse wraps API responses with data or error.
type ApiResponse[T any] struct {
	Data    T                 `json:"data"`
	Error   *ApiError         `json:"error,omitempty"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
}

// ---------- Database Types ----------

// QueryError represents a database query error. It implements the error
// interface and supports errors.Is for sentinel matching.
type QueryError struct {
	Message   string `json:"message"`
	Code      string `json:"code,omitempty"`
	Details   string `json:"details,omitempty"`
	Status    int    `json:"status,omitempty"`
	Operation string `json:"operation,omitempty"`
	sentinel  error
}

// Error returns the error message.
func (e *QueryError) Error() string {
	if e.Operation != "" {
		return fmt.Sprintf("%s: %s", e.Operation, e.Message)
	}
	return e.Message
}

// Unwrap returns the sentinel error for use with errors.Is.
func (e *QueryError) Unwrap() error {
	return e.sentinel
}

// QueryResult wraps database query results.
type QueryResult[T any] struct {
	Data   T           `json:"data"`
	Error  *QueryError `json:"error,omitempty"`
	Status int         `json:"status"`
	Count  *int        `json:"count,omitempty"`
}

// FilterOperator defines filter operators for query builder.
type FilterOperator string

const (
	FilterEq          FilterOperator = "eq"
	FilterNeq         FilterOperator = "neq"
	FilterGt          FilterOperator = "gt"
	FilterGte         FilterOperator = "gte"
	FilterLt          FilterOperator = "lt"
	FilterLte         FilterOperator = "lte"
	FilterLike        FilterOperator = "like"
	FilterILike       FilterOperator = "ilike"
	FilterIs          FilterOperator = "is"
	FilterIn          FilterOperator = "in"
	FilterContains    FilterOperator = "cs"
	FilterContainedBy FilterOperator = "cd"
	FilterOverlaps    FilterOperator = "ov"
	FilterFTS         FilterOperator = "fts"
)

// OrderDirection defines sort direction.
type OrderDirection string

const (
	OrderAsc  OrderDirection = "asc"
	OrderDesc OrderDirection = "desc"
)

// ---------- Auth Types ----------

// User represents an authenticated user.
type User struct {
	ID               string         `json:"id"`
	Email            string         `json:"email,omitempty"`
	Phone            string         `json:"phone,omitempty"`
	EmailConfirmedAt *time.Time     `json:"emailConfirmedAt,omitempty"`
	PhoneConfirmedAt *time.Time     `json:"phoneConfirmedAt,omitempty"`
	LastSignInAt     *time.Time     `json:"lastSignInAt,omitempty"`
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        *time.Time     `json:"updatedAt,omitempty"`
	UserMetadata     map[string]any `json:"userMetadata,omitempty"`
	AppMetadata      map[string]any `json:"appMetadata,omitempty"`
}

// Session represents a user session.
type Session struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"`
	ExpiresAt    int    `json:"expiresAt"`
	TokenType    string `json:"tokenType"`
	User         *User  `json:"user,omitempty"`
}

// AuthError represents an authentication error. It supports errors.Is for
// sentinel error matching.
type AuthError struct {
	Message string `json:"message"`
	Status  int    `json:"status,omitempty"`
	sentinel error
}

// Error returns the error message.
func (e *AuthError) Error() string {
	return e.Message
}

// Unwrap returns the sentinel error for use with errors.Is.
func (e *AuthError) Unwrap() error {
	return e.sentinel
}

// AuthResponse wraps authentication responses.
type AuthResponse struct {
	Session *Session   `json:"session,omitempty"`
	User    *User      `json:"user,omitempty"`
	Error   *AuthError `json:"error,omitempty"`
}

// OAuthProvider defines supported OAuth providers.
type OAuthProvider string

const (
	OAuthGoogle    OAuthProvider = "google"
	OAuthGitHub    OAuthProvider = "github"
	OAuthApple     OAuthProvider = "apple"
	OAuthMicrosoft OAuthProvider = "microsoft"
	OAuthFacebook  OAuthProvider = "facebook"
	OAuthTwitter   OAuthProvider = "twitter"
)

// AuthChangeEvent defines auth state change events.
type AuthChangeEvent string

const (
	AuthEventInitialSession   AuthChangeEvent = "INITIAL_SESSION"
	AuthEventSignedIn         AuthChangeEvent = "SIGNED_IN"
	AuthEventSignedOut        AuthChangeEvent = "SIGNED_OUT"
	AuthEventTokenRefreshed   AuthChangeEvent = "TOKEN_REFRESHED"
	AuthEventUserUpdated      AuthChangeEvent = "USER_UPDATED"
	AuthEventPasswordRecovery AuthChangeEvent = "PASSWORD_RECOVERY"
)

// ---------- Storage Types ----------

// FileObject represents a file's metadata.
type FileObject struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	BucketID    string         `json:"bucketId"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   *time.Time     `json:"updatedAt,omitempty"`
	Size        int64          `json:"size"`
	ContentType string         `json:"contentType,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// Bucket represents a storage bucket.
type Bucket struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Public           bool       `json:"public"`
	FileSizeLimit    *int64     `json:"fileSizeLimit,omitempty"`
	AllowedMimeTypes []string   `json:"allowedMimeTypes,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        *time.Time `json:"updatedAt,omitempty"`
}

// UploadResult represents a file upload result.
type UploadResult struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	FullPath    string `json:"fullPath"`
	Size        int64  `json:"size"`
	ContentType string `json:"contentType,omitempty"`
}

// StorageError represents a storage operation error. It supports errors.Is for
// sentinel error matching.
type StorageError struct {
	Message    string `json:"message"`
	StatusCode string `json:"statusCode"`
	Err        string `json:"error"`
	sentinel   error
}

// Error returns the error message.
func (e *StorageError) Error() string {
	return e.Message
}

// Unwrap returns the sentinel error for use with errors.Is.
func (e *StorageError) Unwrap() error {
	return e.sentinel
}

// ImageTransformOptions configures image transformations.
type ImageTransformOptions struct {
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Resize  string `json:"resize,omitempty"`  // "cover", "contain", "fill"
	Format  string `json:"format,omitempty"`  // "webp", "png", "jpeg", "avif"
	Quality int    `json:"quality,omitempty"` // 1-100
}

// UploadOptions configures file uploads.
type UploadOptions struct {
	ContentType  string         `json:"contentType,omitempty"`
	CacheControl string         `json:"cacheControl,omitempty"`
	Upsert       bool           `json:"upsert,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// ListFilesOptions configures file listing.
type ListFilesOptions struct {
	Limit  int               `json:"limit,omitempty"`
	Offset int               `json:"offset,omitempty"`
	Search string            `json:"search,omitempty"`
	SortBy map[string]string `json:"sortBy,omitempty"`
}

// SignedURLOptions configures signed URL generation.
type SignedURLOptions struct {
	ExpiresIn int                    `json:"expiresIn"`
	Transform *ImageTransformOptions `json:"transform,omitempty"`
	Download  any                    `json:"download,omitempty"` // bool or string
}

// ProgressFunc is a callback for reporting upload/download progress.
// bytesTransferred is the number of bytes transferred so far,
// totalBytes is the total size (-1 if unknown).
type ProgressFunc func(bytesTransferred, totalBytes int64)

// ---------- Functions Types ----------

// FunctionError represents a function invocation error. It supports errors.Is
// for sentinel error matching.
type FunctionError struct {
	Message  string `json:"message"`
	Status   int    `json:"status,omitempty"`
	Name     string `json:"name,omitempty"`
	sentinel error
}

// Error returns the error message.
func (e *FunctionError) Error() string {
	if e.Name != "" {
		return fmt.Sprintf("function %s: %s", e.Name, e.Message)
	}
	return e.Message
}

// Unwrap returns the sentinel error for use with errors.Is.
func (e *FunctionError) Unwrap() error {
	return e.sentinel
}

// FunctionResponse wraps function invocation results.
type FunctionResponse[T any] struct {
	Data  T              `json:"data"`
	Error *FunctionError `json:"error,omitempty"`
}

// FunctionInvokeOptions configures function invocation.
type FunctionInvokeOptions struct {
	Method  string            `json:"method,omitempty"` // GET, POST, PUT, PATCH, DELETE
	Headers map[string]string `json:"headers,omitempty"`
	Timeout int               `json:"timeout,omitempty"` // milliseconds
}

// ---------- AI Types ----------

// CopilotModel defines AI model tiers.
type CopilotModel string

const (
	CopilotQuick      CopilotModel = "copilot-quick"
	CopilotBalanced   CopilotModel = "copilot-balanced"
	CopilotPower      CopilotModel = "copilot-power"
	CopilotEnterprise CopilotModel = "copilot-enterprise"
)

// AIGenerateOptions configures AI generation.
type AIGenerateOptions struct {
	Model       CopilotModel `json:"model,omitempty"`
	Temperature *float64     `json:"temperature,omitempty"`
	MaxTokens   int          `json:"maxTokens,omitempty"`
	Stream      bool         `json:"stream,omitempty"`
}

// SchemaGenerationRequest defines a schema generation request.
type SchemaGenerationRequest struct {
	Prompt           string `json:"prompt"`
	Format           string `json:"format,omitempty"`            // "drizzle", "prisma", "sql", "json"
	IncludeRelations bool   `json:"includeRelations,omitempty"`
	IncludeIndexes   bool   `json:"includeIndexes,omitempty"`
	Database         string `json:"database,omitempty"`          // "postgresql", "mysql", "sqlite"
}

// GeneratedColumn represents a generated database column.
type GeneratedColumn struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Nullable     bool   `json:"nullable,omitempty"`
	PrimaryKey   bool   `json:"primaryKey,omitempty"`
	Unique       bool   `json:"unique,omitempty"`
	DefaultValue string `json:"defaultValue,omitempty"`
}

// GeneratedIndex represents a generated database index.
type GeneratedIndex struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique,omitempty"`
}

// GeneratedRelation represents a generated database relation.
type GeneratedRelation struct {
	Name          string   `json:"name"`
	TargetTable   string   `json:"targetTable"`
	SourceColumns []string `json:"sourceColumns"`
	TargetColumns []string `json:"targetColumns"`
	Type          string   `json:"type"` // "one-to-one", "one-to-many", "many-to-many"
}

// GeneratedTable represents a generated database table.
type GeneratedTable struct {
	Name      string              `json:"name"`
	Columns   []GeneratedColumn   `json:"columns"`
	Indexes   []GeneratedIndex    `json:"indexes,omitempty"`
	Relations []GeneratedRelation `json:"relations,omitempty"`
}

// SchemaGenerationResult wraps schema generation results.
type SchemaGenerationResult struct {
	Schema      string           `json:"schema"`
	Format      string           `json:"format"`
	Tables      []GeneratedTable `json:"tables"`
	Types       string           `json:"types,omitempty"`
	Explanation string           `json:"explanation,omitempty"`
	CreditsUsed int              `json:"creditsUsed"`
}

// FunctionGenerationRequest defines a function generation request.
type FunctionGenerationRequest struct {
	Prompt       string `json:"prompt"`
	Name         string `json:"name,omitempty"`
	Runtime      string `json:"runtime,omitempty"` // "edge", "node"
	IncludeTypes bool   `json:"includeTypes,omitempty"`
}

// FunctionGenerationResult wraps function generation results.
type FunctionGenerationResult struct {
	Code         string         `json:"code"`
	Name         string         `json:"name"`
	Types        string         `json:"types,omitempty"`
	InputSchema  map[string]any `json:"inputSchema,omitempty"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
	Explanation  string         `json:"explanation,omitempty"`
	CreditsUsed  int            `json:"creditsUsed"`
}

// EndpointGenerationRequest defines an endpoint generation request.
type EndpointGenerationRequest struct {
	Prompt            string `json:"prompt"`
	Method            string `json:"method,omitempty"` // GET, POST, PUT, PATCH, DELETE
	Path              string `json:"path,omitempty"`
	RequiresAuth      bool   `json:"requiresAuth,omitempty"`
	IncludeValidation bool   `json:"includeValidation,omitempty"`
}

// EndpointGenerationResult wraps endpoint generation results.
type EndpointGenerationResult struct {
	Code           string         `json:"code"`
	Method         string         `json:"method"`
	Path           string         `json:"path"`
	RequestSchema  map[string]any `json:"requestSchema,omitempty"`
	ResponseSchema map[string]any `json:"responseSchema,omitempty"`
	OpenAPISpec    map[string]any `json:"openapiSpec,omitempty"`
	Explanation    string         `json:"explanation,omitempty"`
	CreditsUsed    int            `json:"creditsUsed"`
}

// AIChatMessage represents an AI chat message.
type AIChatMessage struct {
	Role    string `json:"role"` // "user", "assistant", "system"
	Content string `json:"content"`
}

// AIChatRequest defines an AI chat request.
type AIChatRequest struct {
	Messages []AIChatMessage `json:"messages"`
	Context  map[string]any  `json:"context,omitempty"`
}

// AISuggestion represents an AI suggestion.
type AISuggestion struct {
	Type        string `json:"type"` // "schema", "function", "endpoint", "query"
	Title       string `json:"title"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

// AIChatResponse wraps AI chat responses.
type AIChatResponse struct {
	Message     AIChatMessage  `json:"message"`
	Suggestions []AISuggestion `json:"suggestions,omitempty"`
	CreditsUsed int            `json:"creditsUsed"`
}

// AICreditsInfo holds AI credits information.
type AICreditsInfo struct {
	Balance           int    `json:"balance"`
	MonthlyAllocation int    `json:"monthlyAllocation"`
	UsedThisMonth     int    `json:"usedThisMonth"`
	ResetDate         string `json:"resetDate"`
}

// AIError represents an AI operation error. It supports errors.Is for sentinel
// error matching.
type AIError struct {
	Message  string `json:"message"`
	Code     string `json:"code,omitempty"`
	Status   int    `json:"status,omitempty"`
	sentinel error
}

// Error returns the error message.
func (e *AIError) Error() string {
	return e.Message
}

// Unwrap returns the sentinel error for use with errors.Is.
func (e *AIError) Unwrap() error {
	return e.sentinel
}

// ---------- Realtime Types ----------

// ChannelStatus represents channel subscription status.
type ChannelStatus string

const (
	ChannelSubscribed ChannelStatus = "SUBSCRIBED"
	ChannelClosed     ChannelStatus = "CLOSED"
	ChannelErrored    ChannelStatus = "ERRORED"
	ChannelTimedOut   ChannelStatus = "TIMED_OUT"
)

// ConnectionState represents the realtime connection state.
type ConnectionState string

const (
	ConnectionDisconnected ConnectionState = "DISCONNECTED"
	ConnectionConnecting   ConnectionState = "CONNECTING"
	ConnectionConnected    ConnectionState = "CONNECTED"
	ConnectionReconnecting ConnectionState = "RECONNECTING"
)

// ConnectionStateCallback is called when the realtime connection state changes.
type ConnectionStateCallback func(state ConnectionState)

// RealtimeOptions configures the realtime connection.
type RealtimeOptions struct {
	Heartbeat            bool                    `json:"heartbeat"`
	HeartbeatInterval    int                     `json:"heartbeatInterval"`    // milliseconds
	AutoReconnect        bool                    `json:"autoReconnect"`
	ReconnectInterval    int                     `json:"reconnectInterval"`    // milliseconds
	MaxReconnectAttempts int                     `json:"maxReconnectAttempts"`
	OnConnect            ConnectionStateCallback `json:"-"`
	OnDisconnect         ConnectionStateCallback `json:"-"`
	OnError              func(error)             `json:"-"`
}

// DefaultRealtimeOptions returns sensible defaults.
func DefaultRealtimeOptions() RealtimeOptions {
	return RealtimeOptions{
		Heartbeat:            true,
		HeartbeatInterval:    30000,
		AutoReconnect:        true,
		ReconnectInterval:    1000,
		MaxReconnectAttempts: 10,
	}
}

// Logger is an interface for custom logging.
type Logger interface {
	Printf(format string, v ...any)
}
