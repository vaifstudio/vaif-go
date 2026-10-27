package vaif_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	vaif "github.com/vaifstudio/vaif-go"
)

// ---------------------------------------------------------------- data

func TestListSerializesFiltersOrderSelectAndPaging(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"data": []any{map[string]any{"id": "1"}}, "count": 42})
	})
	res, err := newClient(t, f).From("todos").
		Select("id", "title").
		Eq("done", false).
		Gte("priority", 2).
		In("tag", "a", "b").
		Order("created_at", true).
		Order("id", false).
		Limit(10).
		Offset(20).
		List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r := f.last()
	if r.Path != "/v1/data/proj_123/todos" {
		t.Fatalf("path %s", r.Path)
	}
	equalJSON(t, []byte(r.Query["where"][0]), `{"AND":[
		{"field":"done","operator":"eq","value":false},
		{"field":"priority","operator":"gte","value":2},
		{"field":"tag","operator":"in","value":["a","b"]}]}`)
	for k, want := range map[string]string{"select": "id,title", "orderBy": "created_at,id", "orderDir": "desc,asc", "limit": "10", "offset": "20"} {
		if got := r.Query[k][0]; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if res.Count != 42 || len(res.Data) != 1 {
		t.Fatalf("res %+v", res)
	}
}

func TestSingleFilterAndRawWhere(t *testing.T) {
	f := newFake(t, listOK)
	c := newClient(t, f)
	if _, err := c.From("todos").ILike("title", "%milk%").List(ctx); err != nil {
		t.Fatal(err)
	}
	equalJSON(t, []byte(f.last().Query["where"][0]), `{"field":"title","operator":"ilike","value":"%milk%"}`)
	or := map[string]any{"OR": []any{map[string]any{"field": "a", "value": 1}, map[string]any{"field": "b", "value": 2}}}
	if _, err := c.From("todos").Where(or).List(ctx); err != nil {
		t.Fatal(err)
	}
	equalJSON(t, []byte(f.last().Query["where"][0]), `{"OR":[{"field":"a","value":1},{"field":"b","value":2}]}`)
}

func TestBuilderErrorsSurfaceAtTerminalAndSendNothing(t *testing.T) {
	f := newFake(t, listOK)
	c := newClient(t, f)
	for name, q := range map[string]*vaif.Query{
		"table":  c.From("todos; drop"),
		"column": c.From("todos").Eq("a b", 1),
		"limit":  c.From("todos").Limit(1001),
		"in":     c.From("todos").In("id"),
		"offset": c.From("todos").Offset(-1),
	} {
		if _, err := q.List(ctx); !errors.Is(err, vaif.ErrInvalidArg) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(f.calls()) != 0 {
		t.Fatal("invalid queries must not reach the network")
	}
}

func TestBuilderIsImmutable(t *testing.T) {
	f := newFake(t, listOK)
	c := newClient(t, f)
	base := c.From("todos").Eq("done", false)
	_ = base.Eq("owner", "u1")
	if _, err := base.List(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.last().Query["where"][0], "owner") {
		t.Fatal("derived query leaked into its base")
	}
}

func TestPaginateAndFirst(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") == "1" {
			writeJSON(w, 200, map[string]any{"data": []any{}, "count": 0})
			return
		}
		rows := make([]any, 10)
		for i := range rows {
			rows[i] = map[string]any{"id": i}
		}
		writeJSON(w, 200, map[string]any{"data": rows, "count": 25})
	})
	c := newClient(t, f)
	page, err := c.From("todos").Paginate(ctx, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if q := f.last().Query; q["limit"][0] != "10" || q["offset"][0] != "10" {
		t.Fatalf("query %v", q)
	}
	if page.Total != 25 || page.PageCount() != 3 || !page.HasNext() || !page.HasPrevious() {
		t.Fatalf("page %+v", page)
	}
	row, err := c.From("todos").First(ctx)
	if err != nil || row != nil {
		t.Fatalf("first: %v %v", row, err)
	}
}

func TestCRUDPathsMethodsAndBodies(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/bulk"):
			writeJSON(w, 200, map[string]any{"data": []any{map[string]any{"id": 1}, map[string]any{"id": 2}}, "count": 2})
		case r.Method == "DELETE":
			writeJSON(w, 200, map[string]any{"data": map[string]any{"id": "n1"}, "deleted": true})
		default:
			writeJSON(w, 200, map[string]any{"data": map[string]any{"id": "n1", "title": "x"}})
		}
	})
	q := newClient(t, f).From("todos")
	if _, err := q.Get(ctx, "a/b"); err != nil {
		t.Fatal(err)
	}
	if p := f.last().Path; p != "/v1/data/proj_123/todos/a%2Fb" {
		t.Fatalf("get path %s", p)
	}
	if row, err := q.Insert(ctx, vaif.Row{"title": "x"}); err != nil || row["id"] != "n1" {
		t.Fatalf("insert %v %v", row, err)
	}
	equalJSON(t, f.last().Body, `{"title":"x"}`)
	if _, err := q.Update(ctx, "n1", map[string]any{"title": "y"}); err != nil {
		t.Fatal(err)
	}
	if r := f.last(); r.Method != "PATCH" || r.Path != "/v1/data/proj_123/todos/n1" {
		t.Fatalf("update %s %s", r.Method, r.Path)
	}
	if err := q.Delete(ctx, "n1"); err != nil || f.last().Method != "DELETE" {
		t.Fatalf("delete %v", err)
	}
	rows, err := q.InsertMany(ctx, []vaif.Row{{"title": "a"}, {"title": "b"}})
	if err != nil || len(rows) != 2 {
		t.Fatalf("bulk %v %v", rows, err)
	}
	equalJSON(t, f.last().Body, `[{"title":"a"},{"title":"b"}]`)
	if _, err := q.Upsert(ctx, vaif.Row{"email": "a@b.c"}, "email"); err != nil {
		t.Fatal(err)
	}
	equalJSON(t, f.last().Body, `{"email":"a@b.c","_upsert":true,"_conflict_columns":["email"]}`)

	var todo struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := q.GetInto(ctx, "n1", &todo); err != nil || todo.Title != "x" {
		t.Fatalf("GetInto %+v %v", todo, err)
	}
}

func TestMalformedSuccessBody(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) })
	_, err := newClient(t, f).From("todos").Insert(ctx, vaif.Row{"title": "x"})
	if !errors.Is(err, vaif.ErrBadResponse) {
		t.Fatalf("got %v", err)
	}
}

// ---------------------------------------------------------------- auth

func loginOK(token, refresh string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Set-Cookie", "project_refresh_token="+refresh+"; Max-Age=2592000; Path=/projects/proj_123/auth; HttpOnly; SameSite=Lax")
		writeJSON(w, 200, map[string]any{"ok": true, "accessToken": token, "expiresIn": 900,
			"user": map[string]any{"id": "u1", "email": "ada@example.com"}})
	}
}

func TestSignInStoresSessionAndRefreshCookie(t *testing.T) {
	f := newFake(t, loginOK("at_1", "rt_1"))
	c := newClient(t, f)
	var events []string
	c.Auth.OnSessionChange(func(e string, _ *vaif.Session) { events = append(events, e) })
	res, err := c.Auth.SignIn(ctx, "ada@example.com", "pw")
	if err != nil || res.Session == nil || res.MFARequired {
		t.Fatalf("%+v %v", res, err)
	}
	equalJSON(t, f.last().Body, `{"email":"ada@example.com","password":"pw"}`)
	s := c.Auth.Session()
	if s.AccessToken != "at_1" || s.RefreshToken != "rt_1" || *s.User.Email != "ada@example.com" {
		t.Fatalf("session %+v", s)
	}
	if left := time.Until(s.ExpiresAt); left < 890*time.Second || left > 900*time.Second {
		t.Fatalf("expiry %s", left)
	}
	if len(events) != 1 || events[0] != vaif.EventSignedIn {
		t.Fatalf("events %v", events)
	}
	f.mu.Lock()
	f.handler = listOK
	f.mu.Unlock()
	if _, err := c.From("todos").List(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.last().Header.Get("Authorization"); got != "Bearer at_1" {
		t.Fatalf("authorization %q", got)
	}
}

func TestMFAFlowAndBadCredentials(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects/proj_123/auth/login":
			if strings.Contains(r.Header.Get("X-Test"), "bad") {
				writeJSON(w, 401, map[string]any{"error": "INVALID_CREDENTIALS", "message": "Invalid email or password"})
				return
			}
			writeJSON(w, 200, map[string]any{"ok": true, "mfaRequired": true, "challengeToken": "ch_1"})
		default:
			loginOK("at_mfa", "rt_mfa")(w, r)
		}
	})
	c := newClient(t, f)
	res, err := c.Auth.SignIn(ctx, "ada@example.com", "pw")
	if err != nil || !res.MFARequired || res.ChallengeToken != "ch_1" || c.Auth.Session() != nil {
		t.Fatalf("%+v %v", res, err)
	}
	s, err := c.Auth.VerifyMFA(ctx, "ch_1", "123456")
	if err != nil || s.AccessToken != "at_mfa" {
		t.Fatalf("%+v %v", s, err)
	}
	equalJSON(t, f.last().Body, `{"challengeToken":"ch_1","code":"123456"}`)

	bad := newClient(t, f, vaif.WithHeader("X-Test", "bad"))
	_, err = bad.Auth.SignIn(ctx, "ada@example.com", "nope")
	var apiErr *vaif.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "INVALID_CREDENTIALS" || bad.Auth.Session() != nil {
		t.Fatalf("bad credentials: %v", err)
	}
}

func TestSignUpSessionAndPendingVerification(t *testing.T) {
	var n int32
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			loginOK("at_new", "rt_new")(w, r)
			return
		}
		writeJSON(w, 201, map[string]any{"ok": true, "pendingVerification": true, "message": "Check your email"})
	})
	c := newClient(t, f)
	res, err := c.Auth.SignUp(ctx, "new@example.com", "pw", &vaif.SignUpOptions{Metadata: map[string]any{"plan": "free"}})
	if err != nil || res.Session == nil || res.Session.AccessToken != "at_new" {
		t.Fatalf("%+v %v", res, err)
	}
	equalJSON(t, f.last().Body, `{"email":"new@example.com","password":"pw","metadata":{"plan":"free"}}`)
	c2 := newClient(t, f)
	res, err = c2.Auth.SignUp(ctx, "taken@example.com", "pw", nil)
	if err != nil || res.Session != nil || !res.PendingVerification || res.Message != "Check your email" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRefreshIsSingleFlightAndRotates(t *testing.T) {
	var refreshes int32
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&refreshes, 1)
		time.Sleep(30 * time.Millisecond)
		loginOK("at_2", "rt_2")(w, r)
	})
	c := newClient(t, f)
	if _, err := c.Auth.SetSession("at_1", "rt_1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	tokens := make([]string, 25)
	for i := range tokens {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := c.Auth.RefreshSession(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			tokens[i] = s.AccessToken
		}(i)
	}
	wg.Wait()
	if refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes)
	}
	for _, tok := range tokens {
		if tok != "at_2" {
			t.Fatalf("tokens %v", tokens)
		}
	}
	if got := f.last().Header.Get("Cookie"); got != "project_refresh_token=rt_1" {
		t.Fatalf("cookie %q", got)
	}
	if _, err := c.Auth.RefreshSession(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.last().Header.Get("Cookie"); got != "project_refresh_token=rt_2" {
		t.Fatalf("rotated cookie %q", got)
	}
}

func TestExpiringSessionRefreshesBeforeRequest(t *testing.T) {
	var refreshes int32
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/auth/refresh") {
			atomic.AddInt32(&refreshes, 1)
			loginOK("at_fresh", "rt_fresh")(w, r)
			return
		}
		listOK(w, r)
	})
	c := newClient(t, f)
	_, _ = c.Auth.SetSession("at_old", "rt_old", time.Now().Add(5*time.Second))
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.From("todos").List(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if refreshes != 1 {
		t.Fatalf("refreshes = %d", refreshes)
	}
	for _, r := range f.calls() {
		if strings.HasSuffix(r.Path, "/todos") && r.Header.Get("Authorization") != "Bearer at_fresh" {
			t.Fatalf("stale token used: %s", r.Header.Get("Authorization"))
		}
	}
}

func TestUnauthorizedTriggersOneRefreshAndRetry(t *testing.T) {
	var me int32
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/auth/refresh") {
			loginOK("at_new", "rt_2")(w, r)
			return
		}
		if atomic.AddInt32(&me, 1) == 1 {
			writeJSON(w, 401, map[string]any{"error": "UNAUTHORIZED", "message": "Invalid or expired token"})
			return
		}
		writeJSON(w, 200, map[string]any{"user": map[string]any{"id": "u1"}})
	})
	c := newClient(t, f)
	_, _ = c.Auth.SetSession("at_revoked", "rt_1", time.Time{})
	u, err := c.Auth.GetUser(ctx)
	if err != nil || u.ID != "u1" {
		t.Fatalf("%v %v", u, err)
	}
	var auths []string
	for _, r := range f.calls() {
		if strings.HasSuffix(r.Path, "/auth/me") {
			auths = append(auths, r.Header.Get("Authorization"))
		}
	}
	if strings.Join(auths, ",") != "Bearer at_revoked,Bearer at_new" {
		t.Fatalf("auths %v", auths)
	}
}

// A request that the API refused with a 401 is sent again only as the session
// it carried. When another user signs in on the client while it is in flight,
// the 401 is returned and nothing is sent with the new user's token.
func TestUnauthorizedIsNotReplayedAsAnotherUser(t *testing.T) {
	var c *vaif.Client
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/auth/me") && r.Header.Get("Authorization") == "Bearer at_user" {
			if _, err := c.Auth.SetSession("at_other", "rt_other", time.Now().Add(time.Hour)); err != nil {
				t.Error(err)
			}
		}
		writeJSON(w, 401, map[string]any{"error": "UNAUTHORIZED", "message": "Invalid or expired token"})
	})
	c = newClient(t, f)
	_, _ = c.Auth.SetSession("at_user", "rt_user", time.Now().Add(time.Hour))

	_, err := c.Auth.GetUser(ctx)
	var apiErr *vaif.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 401 || apiErr.Code != "UNAUTHORIZED" {
		t.Fatalf("GetUser error = %v; want the API's 401 with code UNAUTHORIZED", err)
	}
	calls := f.calls()
	for _, r := range calls {
		if r.Header.Get("Authorization") == "Bearer at_other" {
			t.Errorf("%s %s was sent with Bearer at_other", r.Method, r.Path)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("requests = %d; want 1", len(calls))
	}
	if s := c.Auth.Session(); s == nil || s.AccessToken != "at_other" || s.RefreshToken != "rt_other" {
		t.Fatalf("session = %+v; want the other user's session kept", s)
	}
}

// Guard: after SignOut while the request is in flight, the 401 is returned
// and the request is not sent again without a token.
func TestUnauthorizedAfterSignOutIsNotReplayed(t *testing.T) {
	var c *vaif.Client
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/auth/logout"):
			writeJSON(w, 200, map[string]any{"ok": true})
		case strings.HasSuffix(r.URL.Path, "/auth/me"):
			if err := c.Auth.SignOut(ctx); err != nil {
				t.Error(err)
			}
			writeJSON(w, 401, map[string]any{"error": "UNAUTHORIZED", "message": "Invalid or expired token"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})
	c = newClient(t, f)
	_, _ = c.Auth.SetSession("at_user", "rt_user", time.Now().Add(time.Hour))

	_, err := c.Auth.GetUser(ctx)
	var apiErr *vaif.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 401 || apiErr.Code != "UNAUTHORIZED" {
		t.Fatalf("GetUser error = %v; want the API's 401 with code UNAUTHORIZED", err)
	}
	var paths []string
	for _, r := range f.calls() {
		paths = append(paths, r.Path)
	}
	if got := strings.Join(paths, ","); got != "/projects/proj_123/auth/me,/projects/proj_123/auth/logout" {
		t.Fatalf("requests = %s; want /auth/me then /auth/logout", got)
	}
}

// Guard: when another request's refresh replaced the session while this one
// was in flight, the replay uses the refreshed token without a second refresh.
func TestUnauthorizedAfterAnotherRefreshReplaysWithTheRefreshedToken(t *testing.T) {
	var c *vaif.Client
	var refreshes int32
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/auth/refresh"):
			atomic.AddInt32(&refreshes, 1)
			loginOK("at_new", "rt_2")(w, r)
		case r.Header.Get("Authorization") == "Bearer at_user":
			if _, err := c.Auth.RefreshSession(ctx); err != nil {
				t.Error(err)
			}
			writeJSON(w, 401, map[string]any{"error": "UNAUTHORIZED", "message": "Invalid or expired token"})
		default:
			writeJSON(w, 200, map[string]any{"user": map[string]any{"id": "u1"}})
		}
	})
	c = newClient(t, f)
	_, _ = c.Auth.SetSession("at_user", "rt_user", time.Now().Add(time.Hour))

	u, err := c.Auth.GetUser(ctx)
	if err != nil || u.ID != "u1" {
		t.Fatalf("GetUser = %v, %v; want user u1", u, err)
	}
	var auths []string
	for _, r := range f.calls() {
		if strings.HasSuffix(r.Path, "/auth/me") {
			auths = append(auths, r.Header.Get("Authorization"))
		}
	}
	if got := strings.Join(auths, ","); got != "Bearer at_user,Bearer at_new" {
		t.Fatalf("/auth/me tokens = %s; want Bearer at_user,Bearer at_new", got)
	}
	if n := atomic.LoadInt32(&refreshes); n != 1 {
		t.Fatalf("refreshes = %d; want 1", n)
	}
}

func TestFailedRefreshSignsOut(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 401, map[string]any{"error": "UNAUTHORIZED", "message": "Invalid or expired refresh token"})
	})
	c := newClient(t, f)
	_, _ = c.Auth.SetSession("at", "rt_dead", time.Now().Add(-time.Second))
	var events []string
	c.Auth.OnSessionChange(func(e string, _ *vaif.Session) { events = append(events, e) })
	if _, err := c.From("todos").List(ctx); !errors.Is(err, vaif.ErrAuth) {
		t.Fatalf("got %v", err)
	}
	if c.Auth.Session() != nil || len(events) != 1 || events[0] != vaif.EventSignedOut {
		t.Fatalf("session %v events %v", c.Auth.Session(), events)
	}
}

func TestRefreshWithoutTokenAndGetUserWithoutSession(t *testing.T) {
	f := newFake(t, listOK)
	c := newClient(t, f)
	var apiErr *vaif.Error
	if _, err := c.Auth.GetUser(ctx); !errors.As(err, &apiErr) || apiErr.Code != "NOT_SIGNED_IN" {
		t.Fatalf("got %v", err)
	}
	_, _ = c.Auth.SetSession("at_only", "", time.Time{})
	if _, err := c.Auth.RefreshSession(ctx); !errors.As(err, &apiErr) || apiErr.Code != "NO_REFRESH_TOKEN" {
		t.Fatalf("got %v", err)
	}
	if len(f.calls()) != 0 {
		t.Fatal("no request expected")
	}
}

func TestSignOutRevokesAndClearsEvenWhenExpired(t *testing.T) {
	var n int32
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			writeJSON(w, 200, map[string]any{"ok": true})
			return
		}
		writeJSON(w, 401, map[string]any{"error": "UNAUTHORIZED"})
	})
	c := newClient(t, f)
	_, _ = c.Auth.SetSession("at_1", "rt_1", time.Time{})
	if err := c.Auth.SignOut(ctx); err != nil || c.Auth.Session() != nil {
		t.Fatalf("%v", err)
	}
	if f.last().Header.Get("Authorization") != "Bearer at_1" {
		t.Fatal("logout must carry the bearer")
	}
	_, _ = c.Auth.SetSession("at_2", "rt_2", time.Time{})
	if err := c.Auth.SignOut(ctx); err != nil || c.Auth.Session() != nil {
		t.Fatalf("401 on sign-out should be ignored: %v", err)
	}
	if err := c.Auth.SignOut(ctx); err != nil || len(f.calls()) != 2 {
		t.Fatalf("no-op sign-out: %v calls=%d", err, len(f.calls()))
	}
}

// Guard: after the access token has expired, SignOut still names the user to
// the server: the logout carries the refresh token, which the API's logout
// route accepts in the body when the bearer token no longer verifies. SignOut
// does not refresh first.
func TestSignOutAfterTheAccessTokenExpiredSendsTheRefreshToken(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	c := newClient(t, f)
	if _, err := c.Auth.SetSession("at_expired", "rt_live", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := c.Auth.SignOut(ctx); err != nil || c.Auth.Session() != nil {
		t.Fatalf("SignOut = %v, session %+v; want nil and signed out", err, c.Auth.Session())
	}
	calls := f.calls()
	if len(calls) != 1 || calls[0].Method != "POST" || calls[0].Path != "/projects/proj_123/auth/logout" {
		t.Fatalf("requests = %d (first %+v); want only POST /projects/proj_123/auth/logout", len(calls), calls)
	}
	if calls[0].Header.Get("Authorization") != "Bearer at_expired" {
		t.Fatalf("logout Authorization = %q; want Bearer at_expired", calls[0].Header.Get("Authorization"))
	}
	equalJSON(t, calls[0].Body, `{"refreshToken":"rt_live"}`)
}

func TestSessionRoundTrip(t *testing.T) {
	in := &vaif.Session{AccessToken: "at", RefreshToken: "rt", ExpiresAt: time.Unix(1_900_000_000, 0).UTC()}
	raw, err := vaif.MarshalSession(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := vaif.UnmarshalSession(raw)
	if err != nil || out.RefreshToken != "rt" || !out.ExpiresAt.Equal(in.ExpiresAt) {
		t.Fatalf("%+v %v", out, err)
	}
}

// ---------------------------------------------------------------- storage

func TestUploadMintsThenPutsWithoutCredentials(t *testing.T) {
	var puts int32
	var putHeader http.Header
	var putBody string
	objectHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&puts, 1) == 1 {
			w.WriteHeader(503) // transient: the PUT is retried
			return
		}
		putHeader = r.Header.Clone()
		b := new(strings.Builder)
		buf := make([]byte, 64)
		for {
			n, err := r.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		putBody = b.String()
	})
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "url": "http://localhost/objects/bucket/dev/proj/a.png?X-Amz-Signature=x"})
	})
	c := newClient(t, f, vaif.WithHTTPClient(&http.Client{Transport: handlerTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Path, "/objects/") {
			w := httptest.NewRecorder()
			objectHandler(w, r)
			return w.Result(), nil
		}
		return f.roundTrip(r)
	})}))
	_, _ = c.Auth.SetSession("at_user", "rt", time.Time{})
	res, err := c.Storage.Upload(ctx, "avatars/u1.png", []byte("\x89PNG"), &vaif.UploadOptions{ContentType: "image/png", Bucket: "media"})
	if err != nil {
		t.Fatal(err)
	}
	mint := f.last()
	if mint.Path != "/storage/upload-url" || mint.Header.Get("Authorization") != "Bearer at_user" {
		t.Fatalf("mint %s %v", mint.Path, mint.Header)
	}
	equalJSON(t, mint.Body, `{"key":"avatars/u1.png","bucket":"media","sizeBytes":4,"contentType":"image/png"}`)
	if puts != 2 || putBody != "\x89PNG" || putHeader.Get("Content-Type") != "image/png" {
		t.Fatalf("puts=%d body=%q ct=%q", puts, putBody, putHeader.Get("Content-Type"))
	}
	if putHeader.Get("X-Vaif-Key") != "" || putHeader.Get("Authorization") != "" {
		t.Fatal("credentials leaked to the object store")
	}
	if res.Key != "avatars/u1.png" || res.Size != 4 || res.Bucket != "media" {
		t.Fatalf("res %+v", res)
	}
}

func TestUploadMintIsNotRetried(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) })
	_, err := newClient(t, f).Storage.Upload(ctx, "a.txt", []byte("x"), nil)
	if !errors.Is(err, vaif.ErrServer) || len(f.calls()) != 1 {
		t.Fatalf("err=%v calls=%d", err, len(f.calls()))
	}
	for _, key := range []string{"", "/abs", "a/../b", "bad\x00key"} {
		if _, err := newClient(t, f).Storage.Upload(ctx, key, []byte("x"), nil); !errors.Is(err, vaif.ErrInvalidArg) {
			t.Errorf("key %q: %v", key, err)
		}
	}
}

// A signed storage URL must be https (http only for loopback hosts): the
// object's bytes travel to or from it. Anything else is refused with
// INSECURE_STORAGE_URL before any request is sent to it, and the URL, which
// carries a signature, is not repeated in the error.
func TestSignedStorageURLsMustBeHTTPS(t *testing.T) {
	calls := map[string]func(c *vaif.Client) error{
		"CreateSignedURL": func(c *vaif.Client) error { _, err := c.Storage.CreateSignedURL(ctx, "a.txt", nil); return err },
		"CreateSignedUploadURL": func(c *vaif.Client) error {
			_, err := c.Storage.CreateSignedUploadURL(ctx, "a.txt", 1, nil)
			return err
		},
		"Upload":   func(c *vaif.Client) error { _, err := c.Storage.Upload(ctx, "a.txt", []byte("x"), nil); return err },
		"Download": func(c *vaif.Client) error { _, err := c.Storage.Download(ctx, "a.txt", nil); return err },
	}
	for name, call := range calls {
		for _, signed := range []string{"http://store.example/obj?sig=secret", "/obj?sig=secret", "ftp://store.example/obj?sig=secret", "ws://localhost/obj?sig=secret"} {
			f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, 200, map[string]any{"ok": true, "url": signed})
			})
			err := call(newClient(t, f))
			var apiErr *vaif.Error
			if !errors.As(err, &apiErr) || !errors.Is(err, vaif.ErrBadResponse) || apiErr.Code != "INSECURE_STORAGE_URL" {
				t.Errorf("%s with %q: err = %v; want ErrBadResponse with code INSECURE_STORAGE_URL", name, signed, err)
				continue
			}
			if strings.Contains(err.Error(), "secret") {
				t.Errorf("%s with %q: error %q repeats the signed URL", name, signed, err.Error())
			}
			if calls := f.calls(); len(calls) != 1 || !strings.HasPrefix(calls[0].Path, "/storage/") {
				t.Errorf("%s with %q: %d requests; want only the API call that issued the URL", name, signed, len(calls))
			}
		}
	}
}

// Guard: https anywhere, and http on a loopback host, are accepted.
func TestSignedStorageURLsAcceptHTTPSAndLoopbackHTTP(t *testing.T) {
	for _, signed := range []string{"https://store.example/obj?sig=1", "HTTPS://store.example/obj?sig=1", "http://127.0.0.1:9000/obj?sig=1", "http://localhost:9000/obj?sig=1", "http://[::1]:9000/obj?sig=1"} {
		f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, 200, map[string]any{"ok": true, "url": signed})
		})
		if got, err := newClient(t, f).Storage.CreateSignedURL(ctx, "a.txt", nil); err != nil || got != signed {
			t.Errorf("CreateSignedURL with %q = %q, %v; want it returned as issued", signed, got, err)
		}
	}
}

// Download asks POST /storage/download-url for a signed link and reads the
// object from that link without the project key, the session token or
// WithHeader headers.
func TestDownloadReadsASignedLinkWithoutCredentials(t *testing.T) {
	var mints int32
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/storage/download-url":
			if atomic.AddInt32(&mints, 1) == 1 {
				w.WriteHeader(503) // minting a link has no side effects: retried
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["key"] == "missing.txt" {
				writeJSON(w, 404, map[string]any{"error": "NOT_FOUND", "message": "Object not found"})
				return
			}
			writeJSON(w, 200, map[string]any{"ok": true, "url": "http://localhost/objects/" + body["key"].(string) + "?sig=secret"})
		case "/objects/a.txt":
			_, _ = w.Write([]byte("file-bytes"))
		case "/objects/gone.txt":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(404)
			_, _ = w.Write([]byte("<Error><Code>NoSuchKey</Code></Error>"))
		default:
			w.WriteHeader(500)
		}
	})
	c := newClient(t, f, vaif.WithHeader("X-App", "demo"))
	if _, err := c.Auth.SetSession("at_user", "rt_user", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	data, err := c.Storage.Download(ctx, "a.txt", &vaif.ObjectOptions{Bucket: "media"})
	if err != nil || string(data) != "file-bytes" {
		t.Fatalf("Download = %q, %v; want file-bytes", data, err)
	}
	calls := f.calls()
	var paths []string
	for _, r := range calls {
		paths = append(paths, r.Method+" "+r.Path)
	}
	if got := strings.Join(paths, ", "); got != "POST /storage/download-url, POST /storage/download-url, GET /objects/a.txt" {
		t.Fatalf("requests = %s", got)
	}
	equalJSON(t, calls[1].Body, `{"key":"a.txt","bucket":"media"}`)
	if calls[1].Header.Get("Authorization") != "Bearer at_user" || calls[1].Header.Get("X-Vaif-Key") != testKey {
		t.Fatalf("mint headers = %v; want the bearer token and the key", calls[1].Header)
	}
	for _, name := range []string{"X-Vaif-Key", "Authorization", "X-App", "Cookie"} {
		if got := calls[2].Header.Get(name); got != "" {
			t.Errorf("signed link received %s = %q; want none", name, got)
		}
	}

	_, err = c.Storage.Download(ctx, "missing.txt", nil)
	var apiErr *vaif.Error
	if !errors.As(err, &apiErr) || !errors.Is(err, vaif.ErrNotFound) || apiErr.Code != "NOT_FOUND" {
		t.Fatalf("missing object: %v; want ErrNotFound with the API's code NOT_FOUND", err)
	}
	_, err = c.Storage.Download(ctx, "gone.txt", nil)
	if !errors.As(err, &apiErr) || !errors.Is(err, vaif.ErrNotFound) || apiErr.Code != "STORAGE_DOWNLOAD_FAILED" {
		t.Fatalf("object store 404: %v; want ErrNotFound with code STORAGE_DOWNLOAD_FAILED", err)
	}
}

// A signed link that cannot be reached is ErrNetwork about the object store,
// and the link's signature appears nowhere in the error chain.
func TestSignedLinkNetworkErrorKeepsTheSignatureOut(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "url": "https://objects.example.test/a.txt?X-Amz-Signature=secret"})
	})
	c := newClient(t, f, vaif.WithRetryPolicy(vaif.NoRetry()), vaif.WithHTTPClient(&http.Client{Transport: handlerTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "objects.example.test" {
			return nil, errors.New("connection reset by peer")
		}
		return f.roundTrip(r)
	})}))
	for name, call := range map[string]func() error{
		"Download": func() error { _, err := c.Storage.Download(ctx, "a.txt", nil); return err },
		"Upload":   func() error { _, err := c.Storage.Upload(ctx, "a.txt", []byte("x"), nil); return err },
	} {
		err := call()
		var apiErr *vaif.Error
		if !errors.As(err, &apiErr) || !errors.Is(err, vaif.ErrNetwork) || apiErr.Message != "could not reach the object store" {
			t.Fatalf("%s: err = %v; want ErrNetwork, message \"could not reach the object store\"", name, err)
		}
		for e := error(apiErr); e != nil; e = errors.Unwrap(e) {
			if strings.Contains(e.Error(), "secret") {
				t.Fatalf("%s: error chain %q carries the signature", name, e.Error())
			}
		}
	}
}

func TestSignedUploadURLRequiresSizeAndSendsType(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "url": "https://s3/put?sig"})
	})
	c := newClient(t, f)
	u, err := c.Storage.CreateSignedUploadURL(ctx, "a.txt", 10, nil)
	if err != nil || u != "https://s3/put?sig" {
		t.Fatalf("%q %v", u, err)
	}
	equalJSON(t, f.last().Body, `{"key":"a.txt","sizeBytes":10}`)
	if _, err := c.Storage.CreateSignedUploadURL(ctx, "b.txt", 3, &vaif.UploadOptions{Bucket: "docs", ContentType: "text/plain"}); err != nil {
		t.Fatal(err)
	}
	equalJSON(t, f.last().Body, `{"key":"b.txt","bucket":"docs","sizeBytes":3,"contentType":"text/plain"}`)
	for _, size := range []int64{0, -1, 5<<30 + 1} {
		if _, err := c.Storage.CreateSignedUploadURL(ctx, "c.txt", size, nil); !errors.Is(err, vaif.ErrInvalidArg) {
			t.Errorf("size %d: %v", size, err)
		}
	}
	if len(f.calls()) != 2 {
		t.Fatalf("calls=%d", len(f.calls()))
	}
}

// ---------------------------------------------------------------- functions

func TestInvokeSendsEnvelope(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Fn", "1")
		writeJSON(w, 200, map[string]any{"sent": true})
	})
	c := newClient(t, f)
	resp, err := c.Functions.Invoke(ctx, "send-email", map[string]any{"to": "a@b.c", "path": "not-routing"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.last().Path != "/functions/send-email/invoke" {
		t.Fatalf("path %s", f.last().Path)
	}
	equalJSON(t, f.last().Body, `{"method":"POST","path":"/","body":{"to":"a@b.c","path":"not-routing"}}`)
	var out struct{ Sent bool }
	if err := resp.JSON(&out); err != nil || !out.Sent || resp.Status != 200 || resp.Header.Get("X-Fn") != "1" {
		t.Fatalf("%+v %v", resp, err)
	}
	if _, err := c.Functions.Invoke(ctx, "report", nil, &vaif.InvokeOptions{Method: "get", Path: "/daily", Query: map[string]string{"day": "mon"}, Headers: map[string]string{"X-T": "1"}}); err != nil {
		t.Fatal(err)
	}
	equalJSON(t, f.last().Body, `{"method":"GET","path":"/daily","query":{"day":"mon"},"headers":{"X-T":"1"}}`)
}

// invokeCalls lists a fake's requests as "METHOD path [bearer]".
func invokeCalls(f *fake) string {
	var out []string
	for _, r := range f.calls() {
		out = append(out, r.Method+" "+r.Path+" ["+r.Header.Get("Authorization")+"]")
	}
	return strings.Join(out, ", ")
}

// POST /functions/:name/invoke relays the function's own status and marks
// every response a function produced with X-Vaif-Run-Id. A 401 the function
// returned is its answer: it reaches the caller as it is, the session is not
// refreshed, and the function does not run a second time.
func TestInvokeReturnsTheFunctionsOwn401(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/auth/refresh") {
			loginOK("at_new", "rt_new")(w, r)
			return
		}
		w.Header().Set("X-Vaif-Run-Id", "run_1")
		writeJSON(w, 401, map[string]any{"error": "card_declined", "message": "the card was declined"})
	})
	c := newClient(t, f)
	if _, err := c.Auth.SetSession("at_1", "rt_1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	calls := []func() error{
		func() error {
			_, err := c.Functions.Invoke(ctx, "charge", map[string]any{"amount": 5}, nil)
			return err
		},
		func() error {
			_, err := c.Operations.PostFunctionsByFunctionIdOrNameInvoke(ctx, vaif.OperationOptions{Path: map[string]string{"functionIdOrName": "charge"}})
			return err
		},
	}
	for i, call := range calls {
		f.mu.Lock()
		f.requests = nil
		f.mu.Unlock()
		err := call()
		var apiErr *vaif.Error
		if !errors.As(err, &apiErr) || !errors.Is(err, vaif.ErrAuth) || apiErr.Status != 401 || apiErr.Code != "card_declined" {
			t.Fatalf("call %d: err = %v; want the function's 401 with code card_declined", i, err)
		}
		if got := invokeCalls(f); got != "POST /functions/charge/invoke [Bearer at_1]" {
			t.Fatalf("call %d: requests = %s; want the invocation once, and no refresh", i, got)
		}
	}
	if s := c.Auth.Session(); s == nil || s.AccessToken != "at_1" || s.RefreshToken != "rt_1" {
		t.Fatalf("session = %+v; want at_1 / rt_1, unchanged", s)
	}
}

// Guard: a 401 without X-Vaif-Run-Id is the API refusing the access token
// before the function ran, so the session is refreshed and the invocation is
// sent once more.
func TestInvokeRefreshesAfterTheAPIsOwn401(t *testing.T) {
	var invokes int32
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/auth/refresh") {
			loginOK("at_new", "rt_new")(w, r)
			return
		}
		if atomic.AddInt32(&invokes, 1) == 1 {
			writeJSON(w, 401, map[string]any{"error": "UNAUTHORIZED", "message": "Invalid or expired token"})
			return
		}
		w.Header().Set("X-Vaif-Run-Id", "run_2")
		writeJSON(w, 200, map[string]any{"charged": true})
	})
	c := newClient(t, f)
	if _, err := c.Auth.SetSession("at_1", "rt_1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	resp, err := c.Functions.Invoke(ctx, "charge", nil, nil)
	if err != nil || resp.Status != 200 {
		t.Fatalf("Invoke = %+v, %v; want 200 after one refresh", resp, err)
	}
	want := "POST /functions/charge/invoke [Bearer at_1], POST /projects/proj_123/auth/refresh [], POST /functions/charge/invoke [Bearer at_new]"
	if got := invokeCalls(f); got != want {
		t.Fatalf("requests = %s; want %s", got, want)
	}
}

func TestInvokeErrorsAndRetries(t *testing.T) {
	var n int32
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/flaky/") {
			if atomic.AddInt32(&n, 1) == 1 {
				w.WriteHeader(502)
				return
			}
			writeJSON(w, 200, map[string]any{"ok": 1})
			return
		}
		writeJSON(w, 400, map[string]any{"error": "ValidationError", "message": "bad input", "field": "to"})
	})
	c := newClient(t, f)
	_, err := c.Functions.Invoke(ctx, "f", map[string]any{}, nil)
	var apiErr *vaif.Error
	if !errors.As(err, &apiErr) || !errors.Is(err, vaif.ErrValidation) || apiErr.Body.(map[string]any)["field"] != "to" || len(f.calls()) != 1 {
		t.Fatalf("got %v calls=%d", err, len(f.calls()))
	}
	if _, err := c.Functions.Invoke(ctx, "flaky", nil, &vaif.InvokeOptions{Idempotent: true}); err != nil || n != 2 {
		t.Fatalf("idempotent retry: %v n=%d", err, n)
	}
	for _, o := range []*vaif.InvokeOptions{{Method: "TRACE"}, {Path: "no-slash"}} {
		if _, err := c.Functions.Invoke(ctx, "f", nil, o); !errors.Is(err, vaif.ErrInvalidArg) {
			t.Errorf("%+v: %v", o, err)
		}
	}
	if _, err := c.Functions.Invoke(ctx, "", nil, nil); !errors.Is(err, vaif.ErrInvalidArg) {
		t.Error(err)
	}
}
