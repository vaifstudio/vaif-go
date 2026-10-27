package vaif_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	vaif "github.com/vaifstudio/vaif-go"
)

// The SDK keeps sessions in memory and has no store of its own, so no saved
// session is shared by project id: two clients for one project at different
// base URLs never see each other's session.
func TestClientsForOneProjectDoNotShareASession(t *testing.T) {
	one, err := vaif.NewClient(testProject, testKey, vaif.WithBaseURL("https://api.one.example"))
	if err != nil {
		t.Fatal(err)
	}
	two, err := vaif.NewClient(testProject, testKey, vaif.WithBaseURL("https://api.two.example"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := one.Auth.SetSession("at_one", "rt_one", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s := two.Auth.Session(); s != nil {
		t.Fatalf("second client's session = %+v; want nil", s)
	}
	restored, err := vaif.NewClient(testProject, testKey, vaif.WithBaseURL("https://api.one.example"))
	if err != nil {
		t.Fatal(err)
	}
	if s := restored.Auth.Session(); s != nil {
		t.Fatalf("a new client for the same project and base URL has session %+v; want nil until WithSession", s)
	}
}

// A caller persists sessions through OnSessionChange. Every change reaches the
// listener with the session that replaced the old one, or nil when it ended,
// so a store that follows the listener never keeps an older session: a new
// sign-in overwrites it whole (a refresh token is never carried over from
// the session it replaced), and a refused refresh removes it.
func TestSessionListenerSeesEveryChangeInOrder(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/auth/login"):
			var in struct{ Email string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Email == "one@example.com" {
				loginOK("at_one", "rt_one")(w, r)
				return
			}
			// The second sign-in's response sets no refresh cookie.
			writeJSON(w, 200, map[string]any{"accessToken": "at_two", "expiresIn": 900})
		case strings.HasSuffix(r.URL.Path, "/auth/refresh"):
			writeJSON(w, 401, map[string]any{"error": "UNAUTHORIZED", "message": "Invalid or expired refresh token"})
		default:
			w.WriteHeader(404)
		}
	})
	c := newClient(t, f)
	var store []string // what a persisting listener would hold after each event
	c.Auth.OnSessionChange(func(event string, s *vaif.Session) {
		if s == nil {
			store = append(store, event+" <none>")
			return
		}
		store = append(store, event+" "+s.AccessToken+"/"+s.RefreshToken)
	})

	if _, err := c.Auth.SignIn(ctx, "one@example.com", "pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Auth.SignIn(ctx, "two@example.com", "pw"); err != nil {
		t.Fatal(err)
	}
	if s := c.Auth.Session(); s == nil || s.AccessToken != "at_two" || s.RefreshToken != "" {
		t.Fatalf("session = %+v; want at_two with no refresh token", s)
	}
	if _, err := c.Auth.SetSession("at_three", "rt_three", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Auth.RefreshSession(ctx); err == nil {
		t.Fatal("RefreshSession succeeded; want the refused refresh token's error")
	}
	want := "SIGNED_IN at_one/rt_one, SIGNED_IN at_two/, SIGNED_IN at_three/rt_three, SIGNED_OUT <none>"
	if got := strings.Join(store, ", "); got != want {
		t.Fatalf("listener saw %s; want %s", got, want)
	}
	if c.Auth.Session() != nil {
		t.Fatal("session after the refused refresh; want nil")
	}
}
