package vaif_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	vaif "github.com/vaifstudio/vaif-go"
)

// Verament's plan card from the Go SDK: what each call sends, and what a
// refused confirm returns. A build request is answered with its plan card;
// a server confirms or cancels it on the non-streaming routes, with the plan's
// session and the message it was made for, or skips it with AutoConfirm.

const (
	planID    = "11111111-1111-4111-8111-111111111111"
	sessionID = "22222222-2222-4222-8222-222222222222"
	projectID = "33333333-3333-4333-8333-333333333333"
	prompt    = "tables for a todo list"
	token     = "account_token"
)

func planRef() vaif.PlanRef {
	return vaif.PlanRef{ProjectID: projectID, SessionID: sessionID, PlanID: planID, Message: prompt, GenerationTypes: []string{"schema"}}
}

func TestBlueprintChatSendsAutoConfirmWithTheAccountToken(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "sessionId": sessionID, "preflightRequired": map[string]any{"kind": "plan", "plan": map[string]any{"planId": planID, "fileCount": 1}}})
	})
	res, err := newClient(t, f).Verament(token).Chat(ctx, vaif.BlueprintChatInput{ProjectID: projectID, Message: prompt, AutoConfirm: &vaif.AutoConfirm{MaxCredits: 25}})
	if err != nil {
		t.Fatal(err)
	}
	r := f.last()
	if r.Method != "POST" || r.Path != "/ai/copilot/chat" {
		t.Fatalf("%s %s", r.Method, r.Path)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer "+token {
		t.Fatalf("authorization %q", got)
	}
	equalJSON(t, r.Body, `{"projectId":"`+projectID+`","message":"`+prompt+`","autoConfirm":{"maxCredits":25}}`)
	if res.PreflightRequired == nil || res.PreflightRequired.Plan == nil || res.PreflightRequired.Plan.PlanID != planID {
		t.Fatalf("card %+v", res.PreflightRequired)
	}
}

func TestBlueprintChatRefusesACeilingThatIsNotPositive(t *testing.T) {
	f := newFake(t, listOK)
	_, err := newClient(t, f).Verament(token).Chat(ctx, vaif.BlueprintChatInput{ProjectID: projectID, Message: prompt, AutoConfirm: &vaif.AutoConfirm{MaxCredits: 0}})
	if !errors.Is(err, vaif.ErrInvalidArg) {
		t.Fatalf("err %v", err)
	}
	if len(f.calls()) != 0 {
		t.Fatal("sent a request")
	}
}

func TestConfirmPlanPostsThePlanIDInItsSessionWithItsMessage(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{
			"ok": true, "sessionId": sessionID, "planId": planID, "manifestId": "m1",
			"response": map[string]any{"type": "generation", "content": "", "artifacts": map[string]any{"files": []any{map[string]any{"path": "a.sql", "content": "x"}}}},
			"context":  map[string]any{"creditsCharged": 12, "tokensUsed": 900},
			"build":    map[string]any{"status": "completed", "totalFiles": 1, "totalBatches": 1, "durationSeconds": 2, "warnings": []any{}},
		})
	})
	built, err := newClient(t, f).Verament(token).ConfirmPlan(ctx, vaif.ConfirmPlanInput{PlanRef: planRef()})
	if err != nil {
		t.Fatal(err)
	}
	r := f.last()
	if r.Path != "/ai/copilot/chat" {
		t.Fatalf("path %s", r.Path)
	}
	equalJSON(t, r.Body, `{"projectId":"`+projectID+`","sessionId":"`+sessionID+`","message":"`+prompt+`","generationTypes":["schema"],"preflight":{"confirmPlanId":"`+planID+`"}}`)
	if files := built.Files(); len(files) != 1 || files[0].Path != "a.sql" {
		t.Fatalf("files %+v", files)
	}
	if built.Build == nil || built.Build.Status != "completed" || built.Context.CreditsCharged == nil || *built.Context.CreditsCharged != 12 {
		t.Fatalf("build %+v context %+v", built.Build, built.Context)
	}
}

func TestConfirmPlanSendsACreditChoiceAndRefusesStop(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })
	cp := newClient(t, f).Verament(token)
	if _, err := cp.ConfirmPlan(ctx, vaif.ConfirmPlanInput{PlanRef: planRef(), CreditChoice: "until_zero"}); err != nil {
		t.Fatal(err)
	}
	equalJSON(t, []byte(mustField(t, f.last().Body, "preflight")), `{"confirmPlanId":"`+planID+`","creditChoice":"until_zero"}`)
	if _, err := cp.ConfirmPlan(ctx, vaif.ConfirmPlanInput{PlanRef: planRef(), CreditChoice: "stop"}); !errors.Is(err, vaif.ErrInvalidArg) {
		t.Fatalf("err %v", err)
	}
}

func TestARefusedConfirmReturnsItsCodeAndIsNotRetried(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 409, map[string]any{
			"error":   map[string]any{"code": "PLAN_ALREADY_CONFIRMED", "message": "This plan was already confirmed.", "statusCode": 409, "retryable": false},
			"message": "This plan was already confirmed.",
		})
	})
	_, err := newClient(t, f).Verament(token).ConfirmPlan(ctx, vaif.ConfirmPlanInput{PlanRef: planRef()})
	var apiErr *vaif.Error
	if !errors.As(err, &apiErr) || !errors.Is(err, vaif.ErrConflict) || apiErr.Code != "PLAN_ALREADY_CONFIRMED" {
		t.Fatalf("err %v", err)
	}
	if len(f.calls()) != 1 {
		t.Fatalf("calls %d", len(f.calls()))
	}
}

// A confirm is single use, so it is never replayed, even on a status the
// client retries for other requests (a 503 here).
func TestAConfirmIsNeverReplayedEvenOnA503(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 503, map[string]any{
			"error":   map[string]any{"code": "AI_AT_CAPACITY", "message": "Blueprint is at capacity right now.", "statusCode": 503, "retryable": true},
			"message": "Blueprint is at capacity right now.",
		})
	})
	_, err := newClient(t, f).Verament(token).ConfirmPlan(ctx, vaif.ConfirmPlanInput{PlanRef: planRef()})
	var apiErr *vaif.Error
	if !errors.As(err, &apiErr) || !errors.Is(err, vaif.ErrServer) || apiErr.Code != "AI_AT_CAPACITY" {
		t.Fatalf("err %v", err)
	}
	if len(f.calls()) != 1 {
		t.Fatalf("calls %d", len(f.calls()))
	}
}

func TestCancelPlanPostsCancelPlanID(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "sessionId": sessionID, "planId": planID, "stopped": true, "creditsCharged": 0})
	})
	res, err := newClient(t, f).Verament(token).CancelPlan(ctx, planRef())
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, []byte(mustField(t, f.last().Body, "preflight")), `{"cancelPlanId":"`+planID+`"}`)
	if !res.Stopped {
		t.Fatalf("res %+v", res)
	}
}

func TestQueueConfirmPlanAndGetJob(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			writeJSON(w, 200, map[string]any{"ok": true, "jobId": "j1", "status": "queued"})
			return
		}
		writeJSON(w, 200, map[string]any{"jobId": "j1", "status": "completed", "result": map[string]any{"status": "completed", "planId": planID, "files": []any{map[string]any{"path": "a.sql", "content": "x"}}}})
	})
	cp := newClient(t, f).Verament(token)
	job, err := cp.QueueConfirmPlan(ctx, vaif.ConfirmPlanInput{PlanRef: planRef()})
	if err != nil {
		t.Fatal(err)
	}
	if r := f.last(); r.Path != "/ai/copilot/job" || job.JobID != "j1" {
		t.Fatalf("%s %+v", r.Path, job)
	}
	equalJSON(t, []byte(mustField(t, f.last().Body, "preflight")), `{"confirmPlanId":"`+planID+`"}`)
	status, err := cp.GetJob(ctx, "j1")
	if err != nil {
		t.Fatal(err)
	}
	if r := f.last(); r.Method != "GET" || r.Path != "/ai/copilot/job/j1" {
		t.Fatalf("%s %s", r.Method, r.Path)
	}
	if status.Result == nil || status.Result.PlanID != planID || len(status.Result.Files) != 1 {
		t.Fatalf("result %+v", status.Result)
	}
}

func TestGetSessionReturnsTheWaitingCard(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"session": map[string]any{"id": sessionID}, "messages": []any{},
			"pendingPlan": map[string]any{"kind": "plan", "plan": map[string]any{"planId": planID}, "messageId": "m1", "prompt": prompt}})
	})
	s, err := newClient(t, f).Verament(token).GetSession(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if f.last().Path != "/ai/copilot/session/"+sessionID {
		t.Fatalf("path %s", f.last().Path)
	}
	if s.PendingPlan == nil || s.PendingPlan.Plan.PlanID != planID || s.PendingPlan.Prompt != prompt {
		t.Fatalf("pending %+v", s.PendingPlan)
	}
}

func TestBlueprintNeedsAnAccountToken(t *testing.T) {
	f := newFake(t, listOK)
	if _, err := newClient(t, f).Verament("").GetJob(ctx, "j1"); !errors.Is(err, vaif.ErrInvalidArg) {
		t.Fatalf("err %v", err)
	}
	if len(f.calls()) != 0 {
		t.Fatal("sent a request")
	}
}

// mustField returns one top-level field of a JSON body, re-encoded.
func mustField(t *testing.T, raw []byte, key string) string {
	t.Helper()
	m, ok := mustJSON(t, raw).(map[string]any)
	if !ok {
		t.Fatalf("body is not an object: %s", raw)
	}
	b, err := json.Marshal(m[key])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// deadlineRecorder records how long each request was given (its context's
// deadline from the moment it was sent), then hands it to the real transport.
type deadlineRecorder struct {
	mu    sync.Mutex
	given []time.Duration
	next  http.RoundTripper
}

func (d *deadlineRecorder) RoundTrip(r *http.Request) (*http.Response, error) {
	if deadline, ok := r.Context().Deadline(); ok {
		d.mu.Lock()
		d.given = append(d.given, time.Until(deadline))
		d.mu.Unlock()
	}
	return d.next.RoundTrip(r)
}

func TestChatWaitsAsLongAsABuildOnlyWithAutoConfirm(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "sessionId": sessionID})
	})
	rec := &deadlineRecorder{next: handlerTransport(f.roundTrip)}
	cp := newClient(t, f, vaif.WithHTTPClient(&http.Client{Transport: rec})).Verament(token)
	if _, err := cp.Chat(ctx, vaif.BlueprintChatInput{ProjectID: projectID, Message: prompt, AutoConfirm: &vaif.AutoConfirm{MaxCredits: 25}}); err != nil {
		t.Fatal(err)
	}
	if _, err := cp.Chat(ctx, vaif.BlueprintChatInput{ProjectID: projectID, Message: prompt}); err != nil {
		t.Fatal(err)
	}
	if len(rec.given) != 2 {
		t.Fatalf("recorded %d requests", len(rec.given))
	}
	// The build may run in an auto-confirmed call; a plain message only plans.
	if got := rec.given[0]; got <= 2*time.Minute || got > vaif.BuildTimeout {
		t.Fatalf("auto-confirmed chat given %v, want up to %v", got, vaif.BuildTimeout)
	}
	if got := rec.given[1]; got > 2*time.Minute {
		t.Fatalf("plain chat given %v, want at most 2m", got)
	}
}

func TestAPausedBuildAndARetryableJobDecode(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			writeJSON(w, 200, map[string]any{
				"ok": true, "sessionId": sessionID, "planId": planID, "manifestId": "m1",
				"response": map[string]any{"type": "generation", "content": "", "artifacts": map[string]any{"files": []any{}}},
				"context":  map[string]any{"creditsCharged": 9, "tokensUsed": nil},
				"build": map[string]any{
					"status":     "paused",
					"checkpoint": map[string]any{"id": "c1", "resumeToken": "r1", "expiresAt": "2099-01-01T00:00:00.000Z"},
					"stoppedBy":  map[string]any{"code": "AI_CREDITS_EXHAUSTED_MID_GENERATION", "message": "maximum reached"},
				},
			})
			return
		}
		writeJSON(w, 200, map[string]any{"jobId": "j1", "status": "completed", "result": map[string]any{"status": "failed", "code": "COPILOT_PAUSED", "retryable": true}})
	})
	cp := newClient(t, f).Verament(token)
	built, err := cp.ConfirmPlan(ctx, vaif.ConfirmPlanInput{PlanRef: planRef(), CreditChoice: "until_zero"})
	if err != nil {
		t.Fatal(err)
	}
	if built.Build == nil || built.Build.Status != "paused" || built.Build.Checkpoint == nil || built.Build.Checkpoint.ResumeToken != "r1" ||
		built.Build.StoppedBy == nil || built.Build.StoppedBy.Code != "AI_CREDITS_EXHAUSTED_MID_GENERATION" {
		t.Fatalf("build %+v", built.Build)
	}
	job, err := cp.GetJob(ctx, "j1")
	if err != nil {
		t.Fatal(err)
	}
	if job.Result == nil || job.Result.Code != "COPILOT_PAUSED" || !job.Result.Retryable {
		t.Fatalf("result %+v", job.Result)
	}
}

// Verament engines and effort (spec 2026-10-12, final names 2026-10-14): a request names a tier and
// an effort by id, the plan card names the engine its confirm builds, and a
// tier or effort above the plan is a 403 offer. Values hand-typed from the spec.

func TestTierAndEffortIDs(t *testing.T) {
	got := []string{string(vaif.TierLite), string(vaif.TierStandard), string(vaif.TierMax),
		string(vaif.EffortQuick), string(vaif.EffortBalanced), string(vaif.EffortDeep), string(vaif.EffortExtended)}
	want := []string{"lite", "standard", "max", "quick", "balanced", "deep", "extended"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ids %v, want %v", got, want)
		}
	}
}

func TestChatSendsTierAndEffort(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "sessionId": sessionID})
	})
	if _, err := newClient(t, f).Verament(token).Chat(ctx, vaif.BlueprintChatInput{ProjectID: projectID, Message: prompt, Tier: vaif.TierStandard, Effort: vaif.EffortQuick}); err != nil {
		t.Fatal(err)
	}
	equalJSON(t, f.last().Body, `{"projectId":"`+projectID+`","message":"`+prompt+`","tier":"standard","effort":"quick"}`)
}

func TestPlanCardDecodesEngine(t *testing.T) {
	engine := map[string]any{"tier": "standard", "tierLabel": "Verament Standard 1.0", "effort": "deep", "effortLabel": "Deep"}
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "sessionId": sessionID, "preflightRequired": map[string]any{"kind": "plan", "plan": map[string]any{"planId": planID, "fileCount": 1, "engine": engine}}})
	})
	cp := newClient(t, f).Verament(token)
	res, err := cp.Chat(ctx, vaif.BlueprintChatInput{ProjectID: projectID, Message: prompt})
	if err != nil {
		t.Fatal(err)
	}
	card := res.PreflightRequired.Plan
	if card.Engine == nil || *card.Engine != (vaif.PlanEngine{Tier: "standard", TierLabel: "Verament Standard 1.0", Effort: "deep", EffortLabel: "Deep"}) {
		t.Fatalf("engine %+v", card.Engine)
	}

	// A plan made before engines were named has none.
	old := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "preflightRequired": map[string]any{"kind": "plan", "plan": map[string]any{"planId": planID, "engine": nil}}})
	})
	res, err = newClient(t, old).Verament(token).Chat(ctx, vaif.BlueprintChatInput{ProjectID: projectID, Message: prompt})
	if err != nil {
		t.Fatal(err)
	}
	if res.PreflightRequired.Plan.Engine != nil {
		t.Fatalf("engine %+v", res.PreflightRequired.Plan.Engine)
	}
}

// A confirm names no tier or effort: the plan's own choice is what it builds.
func TestConfirmPlanSendsNoTier(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "jobId": "j1"})
	})
	cp := newClient(t, f).Verament(token)
	if _, err := cp.ConfirmPlan(ctx, vaif.ConfirmPlanInput{PlanRef: planRef()}); err != nil {
		t.Fatal(err)
	}
	if _, err := cp.QueueConfirmPlan(ctx, vaif.ConfirmPlanInput{PlanRef: planRef()}); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.calls() {
		var body map[string]any
		if err := json.Unmarshal(r.Body, &body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["tier"]; ok {
			t.Fatalf("%s sent a tier: %s", r.Path, r.Body)
		}
		if _, ok := body["effort"]; ok {
			t.Fatalf("%s sent an effort: %s", r.Path, r.Body)
		}
	}
}

var proMaxOffer = map[string]any{
	"error":         "UpgradeRequired",
	"code":          "BLUEPRINT_TIER_NOT_IN_PLAN",
	"suggestedPlan": "studio_plus",
	"message":       "Verament Max 1.0 is not included in your Pro plan, which includes Verament Lite 1.0 and Verament Standard 1.0 at Quick, Balanced or Deep effort. Studio+ includes it.",
	"allowed":       map[string]any{"tiers": []string{"lite", "standard"}, "efforts": []string{"quick", "balanced", "deep"}},
	"retryWith":     map[string]any{"tier": "standard", "effort": "deep"},
}

func TestBlueprintEngineOfferFromA403(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 403, proMaxOffer) })
	_, err := newClient(t, f).Verament(token).Chat(ctx, vaif.BlueprintChatInput{ProjectID: projectID, Message: prompt, Tier: vaif.TierMax})
	if !errors.Is(err, vaif.ErrAuth) {
		t.Fatalf("err %v", err)
	}
	offer, ok := vaif.BlueprintEngineOfferFrom(err)
	if !ok {
		t.Fatalf("no offer in %v", err)
	}
	want := vaif.EngineOffer{
		Code:          "BLUEPRINT_TIER_NOT_IN_PLAN",
		Message:       proMaxOffer["message"].(string),
		SuggestedPlan: "studio_plus",
	}
	want.Allowed.Tiers = []vaif.BlueprintTier{"lite", "standard"}
	want.Allowed.Efforts = []vaif.BlueprintEffort{"quick", "balanced", "deep"}
	want.RetryWith.Tier, want.RetryWith.Effort = "standard", "deep"
	g, _ := json.Marshal(offer)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Fatalf("offer\n got: %s\nwant: %s", g, w)
	}

	// An effort above the plan is an offer too.
	effortBody := map[string]any{}
	for k, v := range proMaxOffer {
		effortBody[k] = v
	}
	effortBody["code"] = "BLUEPRINT_EFFORT_NOT_IN_PLAN"
	ef := newFake(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 403, effortBody) })
	_, err = newClient(t, ef).Verament(token).Chat(ctx, vaif.BlueprintChatInput{ProjectID: projectID, Message: prompt, Effort: vaif.EffortExtended})
	if offer, ok := vaif.BlueprintEngineOfferFrom(err); !ok || offer.Code != "BLUEPRINT_EFFORT_NOT_IN_PLAN" {
		t.Fatalf("effort offer %+v from %v", offer, err)
	}

	// The offer's message is the body's, whatever the error wrapping it says.
	wrapped := &vaif.Error{Kind: vaif.ErrAuth, Status: 403, Code: "BLUEPRINT_TIER_NOT_IN_PLAN", Message: "wrapped", Body: proMaxOffer}
	if offer, ok := vaif.BlueprintEngineOfferFrom(wrapped); !ok || offer.Message != proMaxOffer["message"] {
		t.Fatalf("message %+v", offer)
	}
}

func TestBlueprintEngineOfferIsFalseForAnyOtherError(t *testing.T) {
	noRetry := map[string]any{}
	for k, v := range proMaxOffer {
		noRetry[k] = v
	}
	noRetry["retryWith"] = map[string]any{"tier": "standard"}
	// The same shape under another code is not an engine offer.
	otherCode := map[string]any{}
	for k, v := range proMaxOffer {
		otherCode[k] = v
	}
	otherCode["code"] = "PLAN_UPGRADE_REQUIRED"
	for _, body := range []map[string]any{
		{"error": "UpgradeRequired", "code": "PLAN_UPGRADE_REQUIRED", "message": "x"},
		noRetry,
		otherCode,
	} {
		f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 403, body) })
		_, err := newClient(t, f).Verament(token).Chat(ctx, vaif.BlueprintChatInput{ProjectID: projectID, Message: prompt})
		if _, ok := vaif.BlueprintEngineOfferFrom(err); ok {
			t.Fatalf("offer from %v", body)
		}
	}
	for _, err := range []error{nil, errors.New("x")} {
		if _, ok := vaif.BlueprintEngineOfferFrom(err); ok {
			t.Fatalf("offer from %v", err)
		}
	}
}

// The plan card's assumptions and the files the workspace provides (DESIGN-v2 §6.2).
func TestPlanCardDecodesAssumptionsAndProvided(t *testing.T) {
	assumption := map[string]any{
		"id": "a1", "topic": "storage", "text": "Post images", "choice": "public",
		"options": []any{
			map[string]any{"value": "public", "label": "public (public)", "affects": "code"},
			map[string]any{"value": "create:media", "label": "Create bucket `media` (public)", "affects": "files"},
		},
	}
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "sessionId": sessionID, "preflightRequired": map[string]any{"kind": "plan", "plan": map[string]any{
			"planId": planID, "fileCount": 1, "assumptions": []any{assumption},
			"provided": []any{map[string]any{"path": "package.json", "reason": "The workspace provides package.json."}},
		}}})
	})
	res, err := newClient(t, f).Verament(token).Chat(ctx, vaif.BlueprintChatInput{ProjectID: projectID, Message: prompt})
	if err != nil {
		t.Fatal(err)
	}
	card := res.PreflightRequired.Plan
	want := []vaif.AssumptionOption{{Value: "public", Label: "public (public)", Affects: "code"}, {Value: "create:media", Label: "Create bucket `media` (public)", Affects: "files"}}
	if len(card.Assumptions) != 1 || card.Assumptions[0].ID != "a1" || card.Assumptions[0].Choice != "public" || len(card.Assumptions[0].Options) != 2 ||
		card.Assumptions[0].Options[0] != want[0] || card.Assumptions[0].Options[1] != want[1] {
		t.Fatalf("assumptions %+v", card.Assumptions)
	}
	if len(card.Provided) != 1 || card.Provided[0] != (vaif.ProvidedFile{Path: "package.json", Reason: "The workspace provides package.json."}) {
		t.Fatalf("provided %+v", card.Provided)
	}
}

// A confirm carries the card's changed choices (code only); Replan plans
// again with choices that change files; PlanWithDefaults plans the message
// that asked a question (DESIGN-v2 §6.2-6.3).
func TestAssumptionChoicesReplanAndPlanWithDefaults(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "sessionId": sessionID, "planWithDefaults": map[string]any{"charged": 0}})
	})
	cp := newClient(t, f).Verament(token)
	if _, err := cp.ConfirmPlan(ctx, vaif.ConfirmPlanInput{PlanRef: planRef(), AssumptionChoices: map[string]string{"a1": "assets"}}); err != nil {
		t.Fatal(err)
	}
	equalJSON(t, []byte(mustField(t, f.last().Body, "preflight")), `{"confirmPlanId":"`+planID+`","assumptionChoices":{"a1":"assets"}}`)
	if _, err := cp.QueueConfirmPlan(ctx, vaif.ConfirmPlanInput{PlanRef: planRef(), AssumptionChoices: map[string]string{"a1": "assets"}}); err != nil {
		t.Fatal(err)
	}
	if r := f.last(); r.Path != "/ai/copilot/job" {
		t.Fatalf("path %s", r.Path)
	}
	equalJSON(t, []byte(mustField(t, f.last().Body, "preflight")), `{"confirmPlanId":"`+planID+`","assumptionChoices":{"a1":"assets"}}`)

	again := vaif.PlanAgainInput{ProjectID: projectID, SessionID: sessionID, Message: prompt}
	if _, err := cp.Replan(ctx, again, map[string]string{"a1": "create:media"}); err != nil {
		t.Fatal(err)
	}
	equalJSON(t, f.last().Body, `{"projectId":"`+projectID+`","sessionId":"`+sessionID+`","message":"`+prompt+`","preflight":{"assumptionChoices":{"a1":"create:media"}}}`)
	if _, err := cp.Replan(ctx, again, nil); !errors.Is(err, vaif.ErrInvalidArg) {
		t.Fatalf("err %v", err)
	}

	res, err := cp.PlanWithDefaults(ctx, again)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, f.last().Body, `{"projectId":"`+projectID+`","sessionId":"`+sessionID+`","message":"`+prompt+`","preflight":{"planWithDefaults":true}}`)
	if res.PlanWithDefaults == nil || res.PlanWithDefaults.Charged != 0 {
		t.Fatalf("planWithDefaults %+v", res.PlanWithDefaults)
	}
}
