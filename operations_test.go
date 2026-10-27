package vaif

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/format"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestGeneratedOperationsRuntime(t *testing.T) {
	calls := 0
	c := parityClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.EscapedPath() != "/projects/a%2Fb" || r.Header.Get("Authorization") != "Bearer token" {
			t.Fatal(r.URL, r.Header)
		}
		return jsonResponse(`{"project":{"id":"p","name":"name"}}`), nil
	})
	c.Auth.SetSession("token", "", time.Time{})
	_, err := c.Operations.GetProjectsByProjectId(context.Background(), OperationOptions{Path: map[string]string{"projectId": "a/b"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Operations.GetProjectsByProjectId(context.Background(), OperationOptions{}); err == nil {
		t.Fatal("missing path accepted")
	}
	// The generated guard refuses on a project client too.
	if _, err = c.Operations.PostAuthSignup(context.Background(), OperationOptions{Body: map[string]string{"email": "a"}}); !errors.Is(err, ErrConsoleOnly) {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
func TestOperationBinaryAndTextBodies(t *testing.T) {
	c := parityClient(t, func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != "raw" || r.Header.Get("Content-Type") != "text/plain" {
			t.Fatal(string(b), r.Header)
		}
		return jsonResponse("raw response"), nil
	})
	resp, err := c.Operations.call(context.Background(), Operation{Method: "POST", Path: "/text", BodyKind: "text", BodyRequired: true}, OperationOptions{Body: "raw"})
	if err != nil || string(resp.body) != "raw response" {
		t.Fatal(resp, err)
	}
	_, err = c.Operations.call(context.Background(), Operation{Method: "POST", Path: "/binary", BodyKind: "binary", BodyRequired: true}, OperationOptions{Body: "not bytes"})
	if err == nil {
		t.Fatal("binary accepted text")
	}
}

func TestGeneratedJSONOperationAcceptsEmptyBody(t *testing.T) {
	c := parityClient(t, func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})
	out, err := callJSON[map[string]any](context.Background(), c.Operations, Operation{Method: "DELETE", Path: "/x"}, OperationOptions{})
	if err != nil || out == nil || *out != nil {
		t.Fatal(out, err)
	}
}

// The generator writes gofmt-canonical Go without running gofmt, so
// regenerating this file needs no Go toolchain. This test fails if a generator
// change breaks that.
func TestGeneratedFileIsGofmtCanonical(t *testing.T) {
	src, err := os.ReadFile("operations_generated.go")
	if err != nil {
		t.Fatal(err)
	}
	formatted, err := format.Source(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(src, formatted) {
		t.Fatal("operations_generated.go is not gofmt-canonical; the maintainers regenerate it (see CONTRIBUTING.md)")
	}
}

// Every operation in the API's OpenAPI description is either a generated
// method (same method and path) or listed in UngeneratedOperations, and
// nothing else. The description is not in this repository: the test runs when
// VAIF_OPENAPI_SPEC names the file, as in the maintainers' CI, and is skipped
// otherwise.
func TestGeneratedTableCoversTheSpec(t *testing.T) {
	specPath := os.Getenv("VAIF_OPENAPI_SPEC")
	if specPath == "" {
		t.Skip("the API's OpenAPI description is not in this repository")
	}
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	var want []string
	for path, item := range spec.Paths {
		if len(path) > 1 { // the generator drops a trailing slash
			path = strings.TrimSuffix(path, "/")
		}
		for method := range item {
			switch method {
			case "get", "put", "post", "delete", "patch", "head", "options":
				want = append(want, strings.ToUpper(method)+" "+path)
			}
		}
	}
	var got []string
	for _, op := range OperationsTable() {
		got = append(got, op.Method+" "+op.Path)
	}
	for _, op := range UngeneratedOperations() {
		got = append(got, op.Method+" "+op.Path)
	}
	sort.Strings(want)
	sort.Strings(got)
	// 943 operations: 775 generated and 168 listed as not generated.
	if len(want) != 943 || len(got) != 943 {
		t.Fatalf("the OpenAPI description has %d operations and the tables %d, want 943 in both", len(want), len(got))
	}
	if strings.Join(want, "\n") != strings.Join(got, "\n") {
		t.Fatalf("generated table (%d) does not match the API's OpenAPI description (%d); the maintainers regenerate operations_generated.go (see CONTRIBUTING.md)", len(got), len(want))
	}
	if len(OperationsTable()) != len(generatedOperationList) {
		t.Fatal("two generated operations share a name")
	}
}
