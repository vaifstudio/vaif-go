package vaif

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// staffOnlyPath matches the paths of the VAIF platform-staff routes,
// /admin and /auth/impersonate and the paths under them.
var staffOnlyPath = regexp.MustCompile(`^/(?:admin|auth/impersonate)(?:/|$)`)

// staffOnlyOutsideAdmin lists the platform-staff operations outside those
// paths: the API answers each only for VAIF platform staff, so the package
// has no method for any of them.
var staffOnlyOutsideAdmin = []string{
	"DELETE /docs/api-endpoints/{id}",
	"DELETE /docs/changelog/{id}",
	"DELETE /docs/examples/{id}",
	"DELETE /docs/sdk-examples/{id}",
	"DELETE /docs/sdks/{id}",
	"DELETE /docs/v2/pages/{id}",
	"DELETE /flags/{key}",
	"GET /ai/copilot/stats",
	"GET /flags",
	"PATCH /docs/api-endpoints/{id}",
	"PATCH /docs/changelog/{id}",
	"PATCH /docs/examples/{id}",
	"PATCH /docs/sdks/{id}",
	"PATCH /docs/v2/pages/{id}",
	"POST /docs/api-endpoints",
	"POST /docs/changelog",
	"POST /docs/examples",
	"POST /docs/sdk-examples",
	"POST /docs/sdks",
	"POST /docs/v2/pages",
	"POST /enterprise/contracts",
	"POST /enterprise/contracts/{contractId}/invoice",
	"POST /enterprise/invoices/{invoiceId}/paid",
	"POST /enterprise/invoices/{invoiceId}/send",
	"POST /flags",
	"PUT /flags/{key}",
}

func TestGeneratedTableHasNoStaffOnlyOperation(t *testing.T) {
	outside := map[string]bool{}
	for _, route := range staffOnlyOutsideAdmin {
		outside[route] = true
	}
	if len(outside) != 26 {
		t.Fatalf("staffOnlyOutsideAdmin has %d distinct routes, want 26", len(outside))
	}

	table := OperationsTable()
	if len(table) != 775 {
		t.Fatalf("OperationsTable() has %d operations, want 775", len(table))
	}
	for name, op := range table {
		if staffOnlyPath.MatchString(op.Path) || outside[op.Method+" "+op.Path] {
			t.Errorf("%s calls %s %s, a platform-staff route", name, op.Method, op.Path)
		}
	}

	ungenerated := UngeneratedOperations()
	if len(ungenerated) != 168 {
		t.Fatalf("UngeneratedOperations() has %d entries, want 168", len(ungenerated))
	}
	staff := 0
	for _, op := range ungenerated {
		isStaff := staffOnlyPath.MatchString(op.Path) || outside[op.Method+" "+op.Path]
		if isStaff != (op.Reason == "platform staff only") {
			t.Errorf("%s %s: reason %q", op.Method, op.Path, op.Reason)
		}
		if op.Reason == "platform staff only" {
			staff++
		}
	}
	if staff != 134 {
		t.Fatalf("%d operations are listed as platform staff only, want 134", staff)
	}
}

func TestStaffOnlyOperationsOutsideAdminHaveNoMethod(t *testing.T) {
	typ := reflect.TypeOf(&OperationsClient{})
	for _, name := range []string{"PostFlags", "PostEnterpriseInvoicesByInvoiceIdPaid"} {
		if _, ok := typ.MethodByName(name); ok {
			t.Errorf("OperationsClient.%s exists; the API answers it only for platform staff", name)
		}
		if _, ok := OperationsTable()[name]; ok {
			t.Errorf("OperationsTable() has %s", name)
		}
	}
	listed := map[string]string{}
	for _, op := range UngeneratedOperations() {
		listed[op.Method+" "+op.Path] = op.Reason
	}
	for _, route := range []string{"POST /flags", "POST /enterprise/invoices/{invoiceId}/paid"} {
		if listed[route] != "platform staff only" {
			t.Errorf("UngeneratedOperations() lists %s with reason %q, want \"platform staff only\"", route, listed[route])
		}
	}
}

func TestNoOperationsMethodCallsAStaffOnlyRoute(t *testing.T) {
	// Every exported method of OperationsClient is a generated operation
	// whose table entry names its route, so a method for a staff route would
	// have to appear in the table.
	table := OperationsTable()
	typ := reflect.TypeOf(&OperationsClient{})
	if typ.NumMethod() != 775 {
		t.Fatalf("OperationsClient has %d exported methods, want 775", typ.NumMethod())
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
