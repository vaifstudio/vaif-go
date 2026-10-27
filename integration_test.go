package vaif_test

// End-to-end run against a real VAIF API. Skipped unless these are set (use a
// throwaway project):
//
//	VAIF_IT_BASE_URL    e.g. http://127.0.0.1:3791
//	VAIF_IT_PROJECT_ID  project id
//	VAIF_IT_API_KEY     project key with crud + storage + functions scopes
//	VAIF_IT_TABLE       table with a text column "name" (default sdk_items)

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	vaif "github.com/vaifstudio/vaif-go"
)

func integrationClient(t *testing.T) *vaif.Client {
	t.Helper()
	base, project, key := os.Getenv("VAIF_IT_BASE_URL"), os.Getenv("VAIF_IT_PROJECT_ID"), os.Getenv("VAIF_IT_API_KEY")
	if base == "" || project == "" || key == "" {
		t.Skip("VAIF_IT_* environment not set")
	}
	c, err := vaif.NewClient(project, key, vaif.WithBaseURL(base), vaif.WithTimeout(20*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func itTable() string {
	if t := os.Getenv("VAIF_IT_TABLE"); t != "" {
		return t
	}
	return "sdk_items"
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func TestIntegrationAuthLifecycle(t *testing.T) {
	c := integrationClient(t)
	email := "go-" + randHex(5) + "@example.com"
	password := "Pw-" + randHex(12)

	up, err := c.Auth.SignUp(ctx, email, password, &vaif.SignUpOptions{Metadata: map[string]any{"sdk": "go"}})
	if err != nil || up.Session == nil || up.Session.RefreshToken == "" {
		t.Fatalf("sign up: %+v %v", up, err)
	}
	if err := c.Auth.SignOut(ctx); err != nil || c.Auth.Session() != nil {
		t.Fatalf("sign out: %v", err)
	}
	if _, err := c.Auth.SignIn(ctx, email, password+"-wrong"); !errors.Is(err, vaif.ErrAuth) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := c.Auth.SignIn(ctx, email, password); err != nil {
		t.Fatalf("sign in: %v", err)
	}
	me, err := c.Auth.GetUser(ctx)
	if err != nil || me.Email == nil || *me.Email != email {
		t.Fatalf("me: %+v %v", me, err)
	}
	first := c.Auth.Session()
	refreshed, err := c.Auth.RefreshSession(ctx)
	if err != nil || refreshed.RefreshToken == "" || refreshed.RefreshToken == first.RefreshToken {
		t.Fatalf("refresh: %+v %v", refreshed, err)
	}
	if u, err := c.Auth.GetUser(ctx); err != nil || u.ID != me.ID {
		t.Fatalf("me after refresh: %v", err)
	}
	if err := c.Auth.SignOut(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = c.Auth.SetSession(refreshed.AccessToken, refreshed.RefreshToken, time.Time{})
	if _, err := c.Auth.RefreshSession(ctx); !errors.Is(err, vaif.ErrAuth) {
		t.Fatalf("revoked refresh token still works: %v", err)
	}
}

func TestIntegrationDataCRUD(t *testing.T) {
	c := integrationClient(t)
	marker := randHex(4)
	table := c.From(itTable())

	created, err := table.Insert(ctx, vaif.Row{"name": marker + "-a"})
	if err != nil || created["name"] != marker+"-a" {
		t.Fatalf("insert: %v %v", created, err)
	}
	id := created["id"].(string)
	more, err := table.InsertMany(ctx, []vaif.Row{{"name": marker + "-b"}, {"name": marker + "-c"}})
	if err != nil || len(more) != 2 {
		t.Fatalf("bulk: %v %v", more, err)
	}
	if got, err := table.Get(ctx, id); err != nil || got["id"] != id {
		t.Fatalf("get: %v %v", got, err)
	}
	mine := table.Like("name", marker+"-%")
	listed, err := mine.Order("name", false).List(ctx)
	if err != nil || listed.Count != 3 || listed.Data[0]["name"] != marker+"-a" || listed.Data[2]["name"] != marker+"-c" {
		t.Fatalf("list: %+v %v", listed, err)
	}
	page, err := mine.Order("name", false).Paginate(ctx, 2, 2)
	if err != nil || page.Total != 3 || len(page.Data) != 1 || page.HasNext() {
		t.Fatalf("page: %+v %v", page, err)
	}
	only, err := mine.Eq("name", marker+"-b").Select("id", "name").List(ctx)
	if err != nil || only.Count != 1 || len(only.Data[0]) != 2 {
		t.Fatalf("filtered: %+v %v", only, err)
	}
	if up, err := table.Update(ctx, id, vaif.Row{"name": marker + "-z"}); err != nil || up["name"] != marker+"-z" {
		t.Fatalf("update: %v %v", up, err)
	}
	for _, row := range append(more, created) {
		if err := table.Delete(ctx, row["id"].(string)); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}
	if _, err := table.Get(ctx, id); !errors.Is(err, vaif.ErrNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
	_, err = table.Eq("no_such_column", 1).List(ctx)
	var apiErr *vaif.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "INVALID_COLUMN" {
		t.Fatalf("invalid column: %v", err)
	}
}

func TestIntegrationStorage(t *testing.T) {
	c := integrationClient(t)
	key := "sdk-it/go-" + randHex(8) + ".txt"
	payload := []byte("hello from the go sdk")
	if _, err := c.Storage.Upload(ctx, key, payload, &vaif.UploadOptions{ContentType: "text/plain"}); err != nil {
		t.Fatalf("upload: %v", err)
	}
	got, err := c.Storage.Download(ctx, key, nil)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("download: %q %v", got, err)
	}
	if u, err := c.Storage.CreateSignedURL(ctx, key, nil); err != nil || len(u) < 8 {
		t.Fatalf("signed url: %q %v", u, err)
	}
	if _, err := c.Storage.Download(ctx, "sdk-it/missing-"+randHex(8)+".txt", nil); !errors.Is(err, vaif.ErrNotFound) {
		t.Fatalf("missing object: %v", err)
	}
}

func TestIntegrationFunctionsNotFound(t *testing.T) {
	c := integrationClient(t)
	if _, err := c.Functions.Invoke(ctx, "missing-"+randHex(4), map[string]any{"x": 1}, nil); !errors.Is(err, vaif.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}
