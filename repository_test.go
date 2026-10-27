package vaif_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The module requires one dependency, at a version with no known advisory,
// and the go directive names the first Go 1.26 release without the
// standard-library advisories govulncheck reports for earlier ones. Changing
// either is a deliberate change to this test.
func TestModuleRequirements(t *testing.T) {
	raw, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	mod := string(raw)
	if got := regexp.MustCompile(`(?m)^go (\S+)$`).FindStringSubmatch(mod); got == nil || got[1] != "1.26.10" {
		t.Fatalf("go directive = %v; want 1.26.10", got)
	}
	var requires []string
	for _, m := range regexp.MustCompile(`(?m)^require (\S+ \S+)$`).FindAllStringSubmatch(mod, -1) {
		requires = append(requires, m[1])
	}
	if strings.Contains(mod, "require (") || strings.Join(requires, ", ") != "github.com/gorilla/websocket v1.5.3" {
		t.Fatalf("requirements = %q; want only github.com/gorilla/websocket v1.5.3", requires)
	}
}

// CI fails on any known vulnerability: govulncheck's default scan fails only
// when vulnerable code is reachable, so the workflow also runs the module
// scan, which fails on any advisory for a required module version or the Go
// release in use.
func TestCIFailsOnAnyKnownVulnerability(t *testing.T) {
	const path = ".github/workflows/ci.yml"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{
		"run: go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...",
		"run: go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -scan module",
	} {
		if !strings.Contains(string(raw), step) {
			t.Errorf("%s has no step %q", path, step)
		}
	}
}
