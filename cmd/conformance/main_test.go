package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	vaif "github.com/vaifstudio/vaif-go"
)

type fakeAPI struct {
	rows    map[string]map[string]any
	object  []byte
	rotated bool
	calls   int
}

func (f *fakeAPI) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calls++
	status := 200
	var body any
	var raw []byte
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/auth/signup"):
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		email := in["email"].(string)
		id := strings.Split(email, "@")[0]
		body = map[string]any{"accessToken": id, "user": map[string]any{"id": id}, "expiresIn": 900}
	case path == "/storage/upload-url":
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		if in["sizeBytes"] != float64(len("VAIF local conformance")) {
			return nil, fmt.Errorf("missing or incorrect upload size")
		}
		body = map[string]any{"url": "http://localhost/objects"}
	case path == "/objects":
		if r.Header.Get("Authorization") != "" || r.Header.Get("x-vaif-key") != "" {
			return nil, fmt.Errorf("credentials sent to the object store")
		}
		switch r.Method {
		case "PUT":
			f.object, _ = io.ReadAll(r.Body)
			raw = []byte{}
		case "GET":
			raw = f.object
		default:
			return nil, fmt.Errorf("object store method %s", r.Method)
		}
	case path == "/storage/download-url":
		body = map[string]any{"url": "http://localhost/objects"}
	case strings.HasPrefix(path, "/buckets/"):
		if r.Header.Get("Authorization") != "Bearer console" {
			return nil, fmt.Errorf("listing requires console actor")
		}
		body = map[string]any{"files": []any{map[string]any{"key": "conformance.txt"}}}
	case strings.HasPrefix(path, "/v1/data/p/notes"):
		actor := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if f.rotated {
			status = 401
			body = map[string]any{"code": "TOKEN_INVALID", "message": "invalid token"}
			break
		}
		id := strings.TrimPrefix(path, "/v1/data/p/notes/")
		switch r.Method {
		case "POST":
			var row map[string]any
			json.NewDecoder(r.Body).Decode(&row)
			row["id"] = "row"
			row["owner_id"] = actor
			f.rows["row"] = row
			body = map[string]any{"data": row}
		case "GET":
			if path == "/v1/data/p/notes" {
				rows := []any{}
				for _, row := range f.rows {
					if row["owner_id"] == actor {
						rows = append(rows, row)
					}
				}
				body = map[string]any{"data": rows, "count": len(rows)}
			} else {
				body = map[string]any{"data": f.rows[id]}
			}
		case "PATCH":
			var values map[string]any
			json.NewDecoder(r.Body).Decode(&values)
			for k, v := range values {
				f.rows[id][k] = v
			}
			body = map[string]any{"data": f.rows[id]}
		case "DELETE":
			delete(f.rows, id)
			body = map[string]any{"ok": true}
		}
	default:
		return nil, fmt.Errorf("unexpected SDK route: %s %s", r.Method, path)
	}
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
}
func lookup(value any, path string) any {
	for _, key := range strings.Split(path, ".") {
		m, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = m[key]
	}
	return value
}
func resolve(value any, state map[string]any) any {
	switch v := value.(type) {
	case string:
		if strings.HasPrefix(v, "$") {
			return lookup(state, v[1:])
		}
	case map[string]any:
		out := map[string]any{}
		for k, item := range v {
			out[k] = resolve(item, state)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = resolve(item, state)
		}
		return out
	}
	return value
}

// The shared conformance cases are not in this repository: the test runs
// when VAIF_CONFORMANCE_CASES names the cases file, as in the maintainers'
// CI, and is skipped otherwise.
func TestSharedCasesFakeTransport(t *testing.T) {
	casesPath := os.Getenv("VAIF_CONFORMANCE_CASES")
	if casesPath == "" {
		t.Skip("the shared conformance cases are not in this repository")
	}
	raw, err := os.ReadFile(casesPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version int
		Cases   []struct {
			ID       string
			Requires string
			Steps    []struct {
				Op, Actor, As string
				Args          map[string]any
				Expect        map[string]any
			}
		}
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != 1 {
		t.Fatal("unsupported case version")
	}
	api := &fakeAPI{rows: map[string]map[string]any{}}
	h := &http.Client{Transport: api}
	clients := map[string]*vaif.Client{}
	for _, test := range doc.Cases {
		t.Run(test.ID, func(t *testing.T) {
			if test.Requires != "" {
				t.Skip("requires live " + test.Requires)
			}
			state := map[string]any{}
			for _, step := range test.Steps {
				args, _ := resolve(step.Args, state).(map[string]any)
				actor := step.Actor
				if actor == "" {
					actor = "service"
				}
				token := ""
				if step.Op == "storageList" {
					actor, token = "console", "console"
				}
				if step.Op == "signUp" {
					args = map[string]any{"email": actor + "@vaif-local.test", "password": "pw"}
				}
				var actual any
				var err error
				if step.Op == "rotateSigningKey" {
					api.rotated = true
					actual = map[string]any{}
				} else {
					actual, err = operate(context.Background(), "http://localhost", h, clients, input{Op: step.Op, Actor: actor, ProjectID: "p", APIKey: "vk_srv_test", Token: token, Table: "notes", Bucket: "default", BucketID: "11111111-1111-4111-8111-111111111111", Args: args})
				}
				if wanted, ok := step.Expect["error"].(map[string]any); ok {
					e, ok := err.(*vaif.Error)
					if !ok || e.Status != int(wanted["status"].(float64)) || e.Code != wanted["code"] {
						t.Fatalf("%s: %v", step.Op, err)
					}
					continue
				}
				if err != nil {
					t.Fatalf("%s: %v", step.Op, err)
				}
				encoded, _ := json.Marshal(actual)
				json.Unmarshal(encoded, &actual)
				for _, path := range asSlice(step.Expect["has"]) {
					if lookup(actual, path.(string)) == nil {
						t.Fatalf("%s: missing %s", step.Op, path)
					}
				}
				if wanted, ok := step.Expect["equals"].(map[string]any); ok {
					for path, value := range wanted {
						if !reflect.DeepEqual(lookup(actual, path), resolve(value, state)) {
							t.Fatalf("%s: mismatch %s", step.Op, path)
						}
					}
				}
				if wanted, ok := step.Expect["length"].(map[string]any); ok {
					for path, length := range wanted {
						if len(asSlice(lookup(actual, path))) != int(length.(float64)) {
							t.Fatalf("%s: wrong length", step.Op)
						}
					}
				}
				if key, ok := step.Expect["includesKey"].(string); ok {
					found := false
					for _, file := range asSlice(lookup(actual, "files")) {
						if lookup(file, "key") == key {
							found = true
						}
					}
					if !found {
						t.Fatal("uploaded key missing")
					}
				}
				if step.As != "" {
					state[step.As] = actual
				}
			}
		})
	}
}
func asSlice(v any) []any { out, _ := v.([]any); return out }
func TestLocalTargets(t *testing.T) {
	for _, target := range []string{"https://remote.test", "http://user:pass@localhost", "file:///etc/passwd"} {
		u, _ := url.Parse(target)
		if localURL(u) == nil {
			t.Fatal("unsafe target accepted")
		}
	}
}
