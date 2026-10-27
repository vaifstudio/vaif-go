package vaif

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// VAIF Verament from a server: the plan card, confirmed or canceled without
// a stream.
//
// A build request is answered with its plan card: the planning call ran and
// is charged (the card shows it); nothing is built, and the build is not
// charged, until the plan is confirmed. ConfirmPlan builds it and returns
// once the build is over; QueueConfirmPlan builds it in the background (poll
// GetJob); CancelPlan drops it. For automation, Chat with AutoConfirm builds
// in the same call when the whole run costs at most MaxCredits, and otherwise
// returns the card marked refused. A plan can be confirmed once, within 30
// minutes, in its session, with the message it was made for.

// BuildTimeout is how long ConfirmPlan (and Chat with AutoConfirm) waits: the
// whole build runs before the answer.
const BuildTimeout = 15 * time.Minute

// VAIF Verament engines and effort.
//
// VAIF Verament runs on three engines, Verament Lite 1.0, Verament Standard
// 1.0 and Verament Max 1.0, each at Quick, Balanced, Deep or Extended effort.
// The org's plan decides which engines and efforts a request may name; one it
// does not include is refused with a 403 before any model call
// (BlueprintEngineOfferFrom reads the offer). A deeper effort uses more
// tokens, so more credits, at the same rate per token.

// BlueprintTier is a Verament engine, by id.
type BlueprintTier string

// BlueprintEffort is a Verament effort level, by id.
type BlueprintEffort string

const (
	// TierLite is Verament Lite 1.0.
	TierLite BlueprintTier = "lite"
	// TierStandard is Verament Standard 1.0.
	TierStandard BlueprintTier = "standard"
	// TierMax is Verament Max 1.0.
	TierMax BlueprintTier = "max"

	// EffortQuick is Quick effort.
	EffortQuick BlueprintEffort = "quick"
	// EffortBalanced is Balanced effort.
	EffortBalanced BlueprintEffort = "balanced"
	// EffortDeep is Deep effort.
	EffortDeep BlueprintEffort = "deep"
	// EffortExtended is Extended effort.
	EffortExtended BlueprintEffort = "extended"
)

// PlanEngine is the engine a plan card's confirm builds, with its names
// ("Verament Standard 1.0", "Deep"). The card's MaximumCredits includes that effort's
// larger token allowance.
type PlanEngine struct {
	Tier        BlueprintTier   `json:"tier"`
	TierLabel   string          `json:"tierLabel"`
	Effort      BlueprintEffort `json:"effort"`
	EffortLabel string          `json:"effortLabel"`
}

// EngineOffer is a tier or effort above the org's plan: what the plan allows,
// the closest engine it includes (RetryWith: send the request again with it),
// and the plan that includes what was asked. Nothing was charged.
type EngineOffer struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	SuggestedPlan string `json:"suggestedPlan"`
	Allowed       struct {
		Tiers   []BlueprintTier   `json:"tiers"`
		Efforts []BlueprintEffort `json:"efforts"`
	} `json:"allowed"`
	RetryWith struct {
		Tier   BlueprintTier   `json:"tier"`
		Effort BlueprintEffort `json:"effort"`
	} `json:"retryWith"`
}

// BlueprintEngineOfferFrom returns the engine offer in an error from Chat,
// ConfirmPlan or QueueConfirmPlan (codes BLUEPRINT_TIER_NOT_IN_PLAN and
// BLUEPRINT_EFFORT_NOT_IN_PLAN), and false for any other error.
func BlueprintEngineOfferFrom(err error) (*EngineOffer, bool) {
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		return nil, false
	}
	if apiErr.Code != "BLUEPRINT_TIER_NOT_IN_PLAN" && apiErr.Code != "BLUEPRINT_EFFORT_NOT_IN_PLAN" {
		return nil, false
	}
	raw, mErr := json.Marshal(apiErr.Body)
	if mErr != nil {
		return nil, false
	}
	var offer EngineOffer
	if json.Unmarshal(raw, &offer) != nil || offer.RetryWith.Tier == "" || offer.RetryWith.Effort == "" {
		return nil, false
	}
	offer.Code = apiErr.Code
	if offer.Message == "" {
		offer.Message = apiErr.Message
	}
	return &offer, true
}

// BlueprintClient calls VAIF Verament for one VAIF account. Verament acts for
// the account, not a project end user, so it carries the account's access
// token.
type BlueprintClient struct {
	client *Client
	token  string
}

// Verament returns VAIF Verament calls made with a VAIF account access token.
func (c *Client) Verament(accountToken string) *BlueprintClient {
	return &BlueprintClient{client: c, token: accountToken}
}

// AutoConfirm is "accept up to MaxCredits": the build runs in the same request
// when the whole run's maximum fits.
type AutoConfirm struct {
	MaxCredits int `json:"maxCredits"`
}

// CreditChoice is offered when the balance is below the run's maximum: confirm
// again with CreditChoice "smaller" or "until_zero".
type CreditChoice struct {
	Scope            string          `json:"scope"`
	MaximumCredits   int             `json:"maximumCredits"`
	RemainingCredits int             `json:"remainingCredits"`
	UpgradePlan      *PlanRefName    `json:"upgradePlan"`
	SmallerRun       *CreditSizedRun `json:"smallerRun"`
	UntilZero        *CreditSizedRun `json:"untilZero"`
}

// PlanRefName names a VAIF plan.
type PlanRefName struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// CreditSizedRun is one credit choice's maximum.
type CreditSizedRun struct {
	MaximumCredits int `json:"maximumCredits"`
	Batches        int `json:"batches,omitempty"`
}

// PlanCard is the plan a build waits on.
type PlanCard struct {
	PlanID           string              `json:"planId"`
	ExpiresAt        string              `json:"expiresAt"`
	Scope            *PlanScope          `json:"scope"`
	Files            []PlannedFile       `json:"files"`
	FileCount        int                 `json:"fileCount"`
	BatchCount       int                 `json:"batchCount"`
	PlanningCredits  int                 `json:"planningCredits"`
	MaximumCredits   *int                `json:"maximumCredits"`
	RemainingCredits *int                `json:"remainingCredits"`
	Credits          *CreditChoice       `json:"credits"`
	AutoConfirm      *AutoConfirmRefusal `json:"autoConfirm,omitempty"`
	ScopeDecisionID  string              `json:"scopeDecisionId,omitempty"`
	Unmetered        bool                `json:"unmetered"`
	// Engine is the tier and effort the confirm builds; nil on a plan made
	// before engines were named.
	Engine *PlanEngine `json:"engine,omitempty"`
	// Assumptions are the plan's open choices (none on a plan made before them).
	Assumptions []PlanAssumption `json:"assumptions,omitempty"`
	// Provided are files the workspace provides, taken out of the plan.
	Provided []ProvidedFile `json:"provided,omitempty"`
}

// PlanAssumption is an open choice a plan made instead of asking, with the
// options the server offers. A change to an option whose Affects is "code" is
// free at confirm (ConfirmPlanInput.AssumptionChoices); one whose Affects is
// "files" makes a new plan (Replan), and planning is charged again.
type PlanAssumption struct {
	ID string `json:"id"`
	// Topic is "storage", "realtime", "auth", "events", "data", "access" or "other".
	Topic string `json:"topic"`
	Text  string `json:"text"`
	// Choice is the option value the plan uses.
	Choice  string             `json:"choice"`
	Options []AssumptionOption `json:"options"`
}

// AssumptionOption is one choice the card offers for an assumption.
type AssumptionOption struct {
	Value   string `json:"value"`
	Label   string `json:"label"`
	Affects string `json:"affects"`
}

// ProvidedFile is a file the workspace provides instead of the plan.
type ProvidedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// PlanScope is the scope that bounds a plan.
type PlanScope struct {
	Scope string `json:"scope"`
	Auto  bool   `json:"auto"`
	Clear bool   `json:"clear"`
}

// PlannedFile is one file on the card.
type PlannedFile struct {
	Path        string `json:"path"`
	Description string `json:"description"`
}

// AutoConfirmRefusal is the request's ceiling, refused because the maximum is above it.
type AutoConfirmRefusal struct {
	MaxCredits int  `json:"maxCredits"`
	Refused    bool `json:"refused"`
}

// PreflightRequired is what a request stopped at: Kind "plan" (the card) or
// "credits" (a confirm's credit choice, for PlanID).
type PreflightRequired struct {
	Kind    string        `json:"kind"`
	Plan    *PlanCard     `json:"plan,omitempty"`
	Credits *CreditChoice `json:"credits,omitempty"`
	PlanID  string        `json:"planId,omitempty"`
}

// BlueprintFile is one file Verament wrote.
type BlueprintFile struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Language string `json:"language,omitempty"`
	Type     string `json:"type,omitempty"`
}

// BlueprintReply is the reply part of a chat or build answer.
type BlueprintReply struct {
	Type      string `json:"type"`
	Content   string `json:"content"`
	Artifacts *struct {
		Files []BlueprintFile `json:"files"`
	} `json:"artifacts,omitempty"`
}

// BlueprintChatInput is a message to Verament. Tier and Effort name the
// engine (TierLite, TierStandard, TierMax) and effort (EffortQuick ...
// EffortExtended); empty, the plan's defaults apply (on Enterprise, the org's).
type BlueprintChatInput struct {
	ProjectID         string          `json:"projectId"`
	SessionID         string          `json:"sessionId,omitempty"`
	Message           string          `json:"message"`
	GenerationTypes   []string        `json:"generationTypes,omitempty"`
	GenerationOptions map[string]any  `json:"generationOptions,omitempty"`
	AutoConfirm       *AutoConfirm    `json:"autoConfirm,omitempty"`
	Tier              BlueprintTier   `json:"tier,omitempty"`
	Effort            BlueprintEffort `json:"effort,omitempty"`
}

// BlueprintChatResponse is Verament's answer. A build request without
// AutoConfirm (or above its ceiling) carries PreflightRequired: the plan card.
type BlueprintChatResponse struct {
	OK                bool               `json:"ok"`
	SessionID         string             `json:"sessionId"`
	MessageID         string             `json:"messageId"`
	Response          BlueprintReply     `json:"response"`
	Context           map[string]any     `json:"context"`
	PreflightRequired *PreflightRequired `json:"preflightRequired,omitempty"`
	// PlanWithDefaults is set when the reply asked a question instead of
	// planning: answer it, or call PlanWithDefaults. Charged is what the
	// question cost (0 when it was not charged).
	PlanWithDefaults *PlanWithDefaultsOffer `json:"planWithDefaults,omitempty"`
}

// PlanWithDefaultsOffer is what a question instead of a plan cost.
type PlanWithDefaultsOffer struct {
	Charged float64 `json:"charged"`
}

// PlanAgainInput plans a message again in its session (Replan, PlanWithDefaults).
type PlanAgainInput struct {
	ProjectID       string
	SessionID       string
	Message         string
	GenerationTypes []string
}

// PlanRef names a plan: its id, its session, and the message it was made for.
type PlanRef struct {
	ProjectID       string
	SessionID       string
	PlanID          string
	Message         string
	GenerationTypes []string
}

// ConfirmPlanInput confirms a plan; CreditChoice ("smaller" or "until_zero")
// answers a credit choice the confirm stopped at. A confirm names no tier or
// effort: the plan's own (PlanCard.Engine) is what it builds.
type ConfirmPlanInput struct {
	PlanRef
	CreditChoice string
	// AssumptionChoices changes the card's assumptions (id to option value)
	// to options that change only code. One that changes files is refused
	// with 409 PLAN_REPLAN_REQUIRED: use Replan.
	AssumptionChoices map[string]string
}

// BuildResponse is a confirm's answer: the build's files once it is over, or
// PreflightRequired (the credit choice) when the balance cannot cover it.
type BuildResponse struct {
	SessionID  string         `json:"sessionId"`
	PlanID     string         `json:"planId"`
	ManifestID string         `json:"manifestId"`
	Response   BlueprintReply `json:"response"`
	Context    struct {
		CreditsCharged *float64 `json:"creditsCharged"`
		TokensUsed     *int     `json:"tokensUsed"`
	} `json:"context"`
	Build *struct {
		// Status is "completed", or "paused" with a Checkpoint to resume from.
		Status          string   `json:"status"`
		TotalFiles      int      `json:"totalFiles"`
		TotalBatches    int      `json:"totalBatches"`
		DurationSeconds float64  `json:"durationSeconds"`
		Warnings        []string `json:"warnings"`
		Checkpoint      *struct {
			ID          string `json:"id"`
			ResumeToken string `json:"resumeToken"`
			ExpiresAt   string `json:"expiresAt"`
		} `json:"checkpoint,omitempty"`
		// StoppedBy says why a paused build stopped when it was not the caller:
		// Run until zero reached its maximum (AI_CREDITS_EXHAUSTED_MID_GENERATION).
		StoppedBy *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"stoppedBy,omitempty"`
	} `json:"build,omitempty"`
	PreflightRequired *PreflightRequired `json:"preflightRequired,omitempty"`
}

// Files is the build's files (none when it stopped at the credit choice).
func (b *BuildResponse) Files() []BlueprintFile {
	if b == nil || b.Response.Artifacts == nil {
		return nil
	}
	return b.Response.Artifacts.Files
}

// CancelResponse is a cancel's answer: nothing ran and nothing more was charged.
type CancelResponse struct {
	SessionID      string  `json:"sessionId"`
	PlanID         string  `json:"planId"`
	Stopped        bool    `json:"stopped"`
	CreditsCharged float64 `json:"creditsCharged"`
}

// JobCreated is a queued job.
type JobCreated struct {
	JobID     string `json:"jobId"`
	Status    string `json:"status"`
	StatusURL string `json:"statusUrl"`
	EventsURL string `json:"eventsUrl"`
}

// JobStatus is a queued job's state and, once finished, its Result.
type JobStatus struct {
	JobID        string     `json:"jobId"`
	Status       string     `json:"status"`
	Result       *JobResult `json:"result"`
	FailedReason string     `json:"failedReason"`
}

// JobResult is a finished job: Status "completed" (Files), "awaiting_confirmation"
// (Plan to confirm, or PreflightRequired), "paused" (ResumeToken) or "failed".
type JobResult struct {
	JobID             string             `json:"jobId"`
	SessionID         string             `json:"sessionId"`
	Status            string             `json:"status"`
	Plan              *PlanCard          `json:"plan,omitempty"`
	PlanID            string             `json:"planId,omitempty"`
	ManifestID        string             `json:"manifestId,omitempty"`
	PreflightRequired *PreflightRequired `json:"preflightRequired,omitempty"`
	Files             []BlueprintFile    `json:"files,omitempty"`
	TokensUsed        int                `json:"tokensUsed,omitempty"`
	CreditsCharged    float64            `json:"creditsCharged,omitempty"`
	ResumeToken       string             `json:"resumeToken,omitempty"`
	Error             string             `json:"error,omitempty"`
	Code              string             `json:"code,omitempty"`
	// Retryable reports a transient failure (Verament paused or busy): nothing
	// ran and nothing was charged, and a confirm's plan still waits. Send the
	// request again.
	Retryable bool `json:"retryable,omitempty"`
}

// PendingPlan is the plan card a session waits on.
type PendingPlan struct {
	Kind      string   `json:"kind"`
	Plan      PlanCard `json:"plan"`
	MessageID *string  `json:"messageId"`
	// Prompt is the message the plan was made for, when it is in the transcript.
	Prompt string `json:"prompt,omitempty"`
}

// BlueprintSession is a session, its messages, and the card it waits on.
type BlueprintSession struct {
	Session     map[string]any   `json:"session"`
	Messages    []map[string]any `json:"messages"`
	PendingPlan *PendingPlan     `json:"pendingPlan"`
}

func (cp *BlueprintClient) call(ctx context.Context, method, path string, body any, timeout time.Duration, out any) error {
	if strings.TrimSpace(cp.token) == "" {
		return invalidArg("Verament needs a VAIF account access token")
	}
	resp, err := cp.client.do(ctx, request{
		method: method,
		path:   path,
		body:   body,
		bearer: cp.token,
		// A confirm is single use: replaying it could only be refused.
		idempotent: boolPtr(method == http.MethodGet),
		timeout:    timeout,
	})
	if err != nil {
		return err
	}
	return decodeJSON(resp, out)
}

// Chat sends a message. A build request returns the plan card
// (PreflightRequired) unless AutoConfirm covers the build.
func (cp *BlueprintClient) Chat(ctx context.Context, in BlueprintChatInput) (*BlueprintChatResponse, error) {
	if in.ProjectID == "" || in.Message == "" {
		return nil, invalidArg("ProjectID and Message are required")
	}
	if in.AutoConfirm != nil && in.AutoConfirm.MaxCredits <= 0 {
		return nil, invalidArg("AutoConfirm.MaxCredits must be a positive whole number")
	}
	// With AutoConfirm the build may run in this call: wait as long as ConfirmPlan.
	timeout := 2 * time.Minute
	if in.AutoConfirm != nil {
		timeout = BuildTimeout
	}
	var out BlueprintChatResponse
	if err := cp.call(ctx, http.MethodPost, "/ai/copilot/chat", in, timeout, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func planRequest(ref PlanRef, preflight map[string]any) (map[string]any, error) {
	if ref.ProjectID == "" || ref.SessionID == "" || ref.PlanID == "" || ref.Message == "" {
		return nil, invalidArg("ProjectID, SessionID, PlanID and Message are required")
	}
	body := map[string]any{
		"projectId": ref.ProjectID,
		"sessionId": ref.SessionID,
		"message":   ref.Message,
		"preflight": preflight,
	}
	if len(ref.GenerationTypes) > 0 {
		body["generationTypes"] = ref.GenerationTypes
	}
	return body, nil
}

func confirmPreflight(in ConfirmPlanInput) (map[string]any, error) {
	preflight := map[string]any{"confirmPlanId": in.PlanID}
	switch in.CreditChoice {
	case "":
	case "smaller", "until_zero":
		preflight["creditChoice"] = in.CreditChoice
	default:
		return nil, invalidArg("CreditChoice must be \"smaller\" or \"until_zero\"")
	}
	if len(in.AssumptionChoices) > 0 {
		preflight["assumptionChoices"] = in.AssumptionChoices
	}
	return preflight, nil
}

func planAgain(ctx context.Context, cp *BlueprintClient, in PlanAgainInput, preflight map[string]any) (*BlueprintChatResponse, error) {
	if in.ProjectID == "" || in.SessionID == "" || in.Message == "" {
		return nil, invalidArg("ProjectID, SessionID and Message are required")
	}
	body := map[string]any{
		"projectId": in.ProjectID,
		"sessionId": in.SessionID,
		"message":   in.Message,
		"preflight": preflight,
	}
	if len(in.GenerationTypes) > 0 {
		body["generationTypes"] = in.GenerationTypes
	}
	var out BlueprintChatResponse
	if err := cp.call(ctx, http.MethodPost, "/ai/copilot/chat", body, 2*time.Minute, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Replan plans the same message again with the card's changed assumptions,
// when a changed option affects files (ConfirmPlan was refused with 409
// PLAN_REPLAN_REQUIRED). The answer carries the new plan card; planning is
// charged again.
func (cp *BlueprintClient) Replan(ctx context.Context, in PlanAgainInput, assumptionChoices map[string]string) (*BlueprintChatResponse, error) {
	if len(assumptionChoices) == 0 {
		return nil, invalidArg("assumptionChoices must not be empty")
	}
	return planAgain(ctx, cp, in, map[string]any{"assumptionChoices": assumptionChoices})
}

// PlanWithDefaults plans the message of a reply that asked a question
// (BlueprintChatResponse.PlanWithDefaults) without asking. Planning is
// charged as usual.
func (cp *BlueprintClient) PlanWithDefaults(ctx context.Context, in PlanAgainInput) (*BlueprintChatResponse, error) {
	return planAgain(ctx, cp, in, map[string]any{"planWithDefaults": true})
}

// ConfirmPlan confirms the plan and builds it, returning once the build is
// over. A refused confirm is an *Error: 404 not found; 409 already confirmed,
// canceled, superseded, another message or PLAN_STALE; 410 expired.
func (cp *BlueprintClient) ConfirmPlan(ctx context.Context, in ConfirmPlanInput) (*BuildResponse, error) {
	preflight, err := confirmPreflight(in)
	if err != nil {
		return nil, err
	}
	body, err := planRequest(in.PlanRef, preflight)
	if err != nil {
		return nil, err
	}
	var out BuildResponse
	if err := cp.call(ctx, http.MethodPost, "/ai/copilot/chat", body, BuildTimeout, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelPlan drops the plan for good. Nothing runs, and nothing more is charged.
func (cp *BlueprintClient) CancelPlan(ctx context.Context, ref PlanRef) (*CancelResponse, error) {
	body, err := planRequest(ref, map[string]any{"cancelPlanId": ref.PlanID})
	if err != nil {
		return nil, err
	}
	var out CancelResponse
	if err := cp.call(ctx, http.MethodPost, "/ai/copilot/chat", body, 0, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// QueueConfirmPlan confirms the plan and builds it in the background. A
// refused confirm is answered at once; otherwise poll GetJob for the result.
func (cp *BlueprintClient) QueueConfirmPlan(ctx context.Context, in ConfirmPlanInput) (*JobCreated, error) {
	preflight, err := confirmPreflight(in)
	if err != nil {
		return nil, err
	}
	body, err := planRequest(in.PlanRef, preflight)
	if err != nil {
		return nil, err
	}
	var out JobCreated
	if err := cp.call(ctx, http.MethodPost, "/ai/copilot/job", body, 0, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetJob returns a queued job's state and, once finished, its result.
func (cp *BlueprintClient) GetJob(ctx context.Context, jobID string) (*JobStatus, error) {
	if jobID == "" {
		return nil, invalidArg("jobID must not be empty")
	}
	var out JobStatus
	if err := cp.call(ctx, http.MethodGet, "/ai/copilot/job/"+url.PathEscape(jobID), nil, 0, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetSession returns a session, its messages, and the plan card it waits on
// (PendingPlan, nil when none can be confirmed).
func (cp *BlueprintClient) GetSession(ctx context.Context, sessionID string) (*BlueprintSession, error) {
	if sessionID == "" {
		return nil, invalidArg("sessionID must not be empty")
	}
	var out BlueprintSession
	if err := cp.call(ctx, http.MethodGet, "/ai/copilot/session/"+url.PathEscape(sessionID), nil, 0, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
