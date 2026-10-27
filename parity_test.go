package vaif

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func parityClient(t *testing.T, handler transportFunc) *Client {
	t.Helper()
	c, err := NewClient("project", "vk_srv_test", WithRetryPolicy(NoRetry()), WithHTTPClient(&http.Client{Transport: handler}))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func jsonResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}
func TestConsoleOnlyBeforeTransport(t *testing.T) {
	c, err := NewPlatformClient(WithRetryPolicy(NoRetry()), WithHTTPClient(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("console-only call sent %s %s", r.Method, r.URL)
		return nil, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		call       func() error
		method     string
		consoleURL string
	}{
		{func() error { _, e := c.Auth.SignUp(context.Background(), "a@b.test", "pw", nil); return e }, "Auth.SignUp()", ConsoleSignUpURL},
		{func() error { return c.Auth.RequestPasswordReset(context.Background(), "a@b.test") }, "Auth.RequestPasswordReset()", ConsolePasswordResetURL},
		{func() error {
			_, e := c.Operations.PostAuthSignup(context.Background(), OperationOptions{Body: map[string]string{"email": "a"}})
			return e
		}, "PostAuthSignup()", ConsoleSignUpURL},
		{func() error {
			_, e := c.Operations.PostAuthForgotPassword(context.Background(), OperationOptions{Body: map[string]string{"email": "a"}})
			return e
		}, "PostAuthForgotPassword()", ConsolePasswordResetURL},
	} {
		err := tc.call()
		d, ok := IsConsoleOnly(err)
		if !ok || !errors.Is(err, ErrConsoleOnly) || d.Method != tc.method || d.ConsoleURL != tc.consoleURL || !strings.Contains(err.Error(), tc.consoleURL) {
			t.Fatalf("%s: expected CONSOLE_ONLY with %s, got %v (%+v)", tc.method, tc.consoleURL, err, d)
		}
	}
}

func TestProjectClientSignsUpAppUsers(t *testing.T) {
	var paths []string
	c := parityClient(t, func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		return jsonResponse(`{"accessToken":"at","expiresIn":900,"user":{"id":"u"}}`), nil
	})
	if _, err := c.Auth.SignUp(context.Background(), "a@b.test", "pw", &SignUpOptions{Name: "Ada"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Auth.RequestPasswordReset(context.Background(), "a@b.test"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, " ") != "/projects/project/auth/signup /projects/project/auth/forgot-password" {
		t.Fatal(paths)
	}
}

func TestPlatformClientSendsNoProjectKey(t *testing.T) {
	c, err := NewPlatformClient(WithSession(&Session{AccessToken: "console"}), WithHTTPClient(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if _, present := r.Header["X-Vaif-Key"]; present || r.Header.Get("Authorization") != "Bearer console" || r.URL.Path != "/orgs" {
			t.Fatalf("%s %v", r.URL, r.Header)
		}
		return jsonResponse(`[]`), nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Operations.call(context.Background(), Operation{Method: "GET", Path: "/orgs"}, OperationOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestRangeAndCount(t *testing.T) {
	c := parityClient(t, func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		if q.Get("limit") != "1" || q.Get("offset") != "" {
			t.Fatalf("query: %v", q)
		}
		return jsonResponse(`{"data":[],"count":19}`), nil
	})
	n, err := c.From("notes").Range(2, 4).Count(context.Background())
	if err != nil || n != 19 {
		t.Fatalf("%d %v", n, err)
	}
}
func TestMFAManagement(t *testing.T) {
	paths := []string{"enroll-start", "enroll-confirm", "disable", "regenerate-codes"}
	i := 0
	c := parityClient(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/projects/project/auth/mfa/"+paths[i] || r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("%s %v", r.URL, r.Header)
		}
		i++
		return jsonResponse(`{"enrollToken":"e","secret":"s","otpauthUri":"otpauth://totp/a","backupCodes":["b"],"ok":true}`), nil
	})
	c.Auth.SetSession("token", "", time.Time{})
	start, err := c.Auth.EnrollMFA(context.Background())
	if err != nil || start.EnrollToken != "e" {
		t.Fatal(start, err)
	}
	codes, err := c.Auth.ConfirmMFA(context.Background(), "e", "123456")
	if err != nil || len(codes.BackupCodes) != 1 {
		t.Fatal(codes, err)
	}
	if err = c.Auth.DisableMFA(context.Background(), "123456"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Auth.RegenerateMFACodes(context.Background(), "123456"); err != nil {
		t.Fatal(err)
	}
}
func TestOAuthPKCEAndExchange(t *testing.T) {
	c := parityClient(t, func(r *http.Request) (*http.Response, error) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if strings.HasSuffix(r.URL.Path, "/start") {
			if b["codeChallenge"] != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" || b["codeChallengeMethod"] != "S256" || b["state"] != "state" {
				t.Fatal(b)
			}
			return jsonResponse(`{"url":"https://provider.test/consent","expiresIn":600}`), nil
		}
		if r.URL.Path != "/projects/project/auth/oauth/token" || b["code"] != "code" || b["codeVerifier"] == nil {
			t.Fatal(r.URL, b)
		}
		return jsonResponse(`{"accessToken":"token","expiresIn":900}`), nil
	})
	start, err := c.Auth.GetOAuthSignInURL(context.Background(), OAuthOptions{Provider: "google", RedirectURL: "http://localhost/callback", State: "state", CodeVerifier: "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Auth.ExchangeOAuthCode(context.Background(), "code", start.CodeVerifier)
	if err != nil || result.Session.AccessToken != "token" {
		t.Fatal(result, err)
	}
}
func TestStorageListDelete(t *testing.T) {
	c := parityClient(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/storage/buckets" {
			return jsonResponse(`{"buckets":[{"id":"b1","name":"assets"}]}`), nil
		}
		if r.URL.Path != "/buckets/b1/files" {
			t.Fatal(r.URL)
		}
		if r.Method == "DELETE" {
			if r.URL.Query().Get("key") != "a/b.txt" {
				t.Fatal(r.URL)
			}
			return jsonResponse(`{"ok":true}`), nil
		}
		if r.URL.Query().Get("maxKeys") != "10" || r.URL.Query().Get("prefix") != "a/" {
			t.Fatal(r.URL)
		}
		return jsonResponse(`{"files":[{"key":"a/b.txt","size":3}],"nextToken":"next","isTruncated":true}`), nil
	})
	res, err := c.Storage.List(context.Background(), &ListObjectsOptions{Bucket: "assets", Prefix: "a/", Limit: 10})
	if err != nil || len(res.Files) != 1 || !res.HasMore || res.NextCursor != "next" {
		t.Fatal(res, err)
	}
	if err = c.Storage.Delete(context.Background(), "a/b.txt", &ObjectOptions{Bucket: "assets"}); err != nil {
		t.Fatal(err)
	}
}
func TestRangeWindow(t *testing.T) {
	c := parityClient(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("limit") != "3" || r.URL.Query().Get("offset") != "2" {
			t.Fatal(r.URL)
		}
		return jsonResponse(`{"data":[],"count":0}`), nil
	})
	if _, err := c.From("notes").Range(2, 4).List(context.Background()); err != nil {
		t.Fatal(err)
	}
}
