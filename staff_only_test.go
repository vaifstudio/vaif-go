package vaif

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// staffOnlyPath matches the VAIF platform-staff routes, /admin and
// /auth/impersonate and the paths under them.
var staffOnlyPath = regexp.MustCompile(`^/(?:admin|auth/impersonate)(?:/|$)`)

func TestGeneratedTableHasNoStaffOnlyOperation(t *testing.T) {
	table := OperationsTable()
	if len(table) != 801 {
		t.Fatalf("OperationsTable() has %d operations, want 801", len(table))
	}
	for name, op := range table {
		if staffOnlyPath.MatchString(op.Path) {
			t.Errorf("%s calls %s %s, a platform-staff route", name, op.Method, op.Path)
		}
	}

	ungenerated := UngeneratedOperations()
	if len(ungenerated) != 142 {
		t.Fatalf("UngeneratedOperations() has %d entries, want 142", len(ungenerated))
	}
	staff := 0
	for _, op := range ungenerated {
		if staffOnlyPath.MatchString(op.Path) != (op.Reason == "platform staff only") {
			t.Errorf("%s %s: reason %q", op.Method, op.Path, op.Reason)
		}
		if op.Reason == "platform staff only" {
			staff++
		}
	}
	if staff != 108 {
		t.Fatalf("%d operations are listed as platform staff only, want 108", staff)
	}
}

func TestNoOperationsMethodCallsAStaffOnlyRoute(t *testing.T) {
	// Every exported method of OperationsClient is a generated operation
	// whose table entry names its route, so a method for a staff route would
	// have to appear in the table.
	table := OperationsTable()
	typ := reflect.TypeOf(&OperationsClient{})
	if typ.NumMethod() != 801 {
		t.Fatalf("OperationsClient has %d exported methods, want 801", typ.NumMethod())
	}
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		op, ok := table[name]
		if !ok {
			t.Errorf("OperationsClient.%s has no table entry", name)
			continue
		}
		if staffOnlyPath.MatchString(op.Path) {
			t.Errorf("OperationsClient.%s calls %s %s, a platform-staff route", name, op.Method, op.Path)
		}
	}

	// No source file of the package names a staff route anywhere except in
	// the list of operations it does not generate. A route is named when a
	// quote or a space precedes it ("/admin/users", "calls GET /admin/users");
	// /auth/me/admin is a route of its own and not a staff route.
	named := regexp.MustCompile(`["\s]/(?:admin|auth/impersonate)(?:[/"{.\s]|$)`)
	listed := regexp.MustCompile(`^\t\{Method: "[A-Z]+", Path: "[^"]+", Reason: "platform staff only"\},$`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for n, line := range strings.Split(string(src), "\n") {
			if named.MatchString(line) && !listed.MatchString(line) {
				t.Errorf("%s:%d names a platform-staff route: %s", file, n+1, strings.TrimSpace(line))
			}
		}
	}
}
