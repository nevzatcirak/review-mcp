package related

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// fp is a modified file with the hunk-only patch of the given lines.
func fp(path string, lines ...string) provider.FilePatch {
	return provider.FilePatch{Path: path, Type: provider.ChangeModified, Patch: strings.Join(lines, "\n") + "\n"}
}

func ids(pairs ...string) []Identifier {
	var out []Identifier
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, Identifier{Kind: IdentifierKind(pairs[i]), Text: pairs[i+1]})
	}
	return out
}

func surfaceFilter(t *testing.T, globs ...string) *filter.Filter {
	t.Helper()
	cfg := config.Defaults()
	cfg.Related.ContractGlobs = globs
	f, err := filter.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestSurface [canary 1]: the changed contract surface of fixture patches
// (spec §2.1, §2.6): a renamed OpenAPI property is gone and its new name,
// which is only on an added line, is not searched; a removed protobuf field
// is gone; a GraphQL field whose type changed is changed; a renamed route
// gives its static prefix; a type-only change is changed; stop words are
// dropped; files that are no contract files give no token.
func TestSurface(t *testing.T) {
	cases := []struct {
		name  string
		files []provider.FilePatch
		want  []Identifier
	}{
		{"OpenAPI rename", []provider.FilePatch{fp("api/openapi.yaml",
			"@@ -10,6 +10,6 @@ components:",
			"     User:",
			"       type: object",
			"       properties:",
			"-        userName:",
			"+        displayName:",
			"           type: string",
			"           description: The name shown to other users.")},
			ids("gone", "userName")},
		{"protobuf field removal", []provider.FilePatch{fp("proto/user.proto",
			"@@ -3,4 +3,3 @@ message User {",
			"   string id = 1;",
			"-  string legacy_email = 2;",
			"   int64 created_at = 3;",
			"   reserved 9;")},
			ids("gone", "legacy_email")},
		{"GraphQL type change", []provider.FilePatch{fp("schema/orders.graphql",
			"@@ -1,4 +1,4 @@",
			" type Order {",
			"-  amountDue: Int!",
			"+  amountDue: Money!",
			"   placedAt: String",
			" }")},
			ids("changed", "amountDue")},
		{"route rename", []provider.FilePatch{fp("openapi.json",
			"@@ -4,3 +4,3 @@",
			"   \"paths\": {",
			"-    \"/users/{id}/profile\": {",
			"+    \"/accounts/{id}/profile\": {",
			"       \"get\": {")},
			ids("gone", "users", "route", "/users", "changed", "profile")},
		{"type-only change", []provider.FilePatch{fp("proto/jobs.proto",
			"@@ -7,2 +7,2 @@ message Job {",
			"-  int32 retry_count = 4;",
			"+  int64 retry_count = 4;",
			" }")},
			ids("changed", "retry_count")},
		{"stop words", []provider.FilePatch{fp("api/openapi.yml",
			"@@ -1,4 +1,2 @@",
			"-      description: The summary of this example, true or false.",
			"-      type: string",
			"-      format: date-time",
			"+      type: integer",
			"       nullable: true")},
			nil},
		{"no contract file", []provider.FilePatch{fp("internal/text/wrap.go",
			"@@ -1,3 +1,2 @@",
			" package text",
			"-var legacyWidth = 80",
			" var width = 72")},
			nil},
		{"changed exported symbols, after the contract names", []provider.FilePatch{
			fp("pkg/users/service.go",
				"@@ -1,3 +1,3 @@",
				" package users",
				"-func FetchUser(id string) (*User, error) {",
				"+func FetchUser(id int64) (*User, error) {",
				" \treturn nil, nil",
				"@@ -20,2 +20,1 @@",
				"-func PurgeCache() {}",
				" var x = 1"),
			fp("pkg/users/service_test.go",
				"@@ -1,2 +1,1 @@",
				"-func TestLegacyFetch(t *testing.T) {}",
				" var y = 2"),
			fp("proto/user.proto",
				"@@ -3,2 +3,1 @@",
				"-  string nick_name = 5;",
				" }"),
		},
			ids("gone", "nick_name", "symbol", "PurgeCache", "symbol", "FetchUser")},
	}
	flt := surfaceFilter(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Surface(c.files, flt, 50)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Surface = %v\nwant      %v", got, c.want)
			}
		})
	}
}

// TestSurfaceRoutes: the static-prefix rule (spec §2.1 item 3): the
// segments before the first "{…}" segment, kept only with a static segment
// of 3 or more characters; only API descriptions in YAML or JSON give
// routes; routes are searched without word boundaries, the others with.
func TestSurfaceRoutes(t *testing.T) {
	for route, want := range map[string]string{
		"/users/{id}":            "/users",
		"/v1/orders/{id}/items":  "/v1/orders",
		"/v1/{id}":               "",
		"/{tenant}/reports":      "",
		"/":                      "",
		"/health":                "/health",
		"/api/v2/invoices/":      "/api/v2/invoices",
		"/ab/cd/{x}":             "",
		"/catalog/{a}/{b}/price": "/catalog",
	} {
		got, ok := staticPrefix(route)
		if got != want || ok != (want != "") {
			t.Errorf("staticPrefix(%q) = %q, %v; want %q", route, got, ok, want)
		}
	}
	files := []provider.FilePatch{
		fp("openapi.yaml", "@@ -1,2 +1,0 @@", "-  '/v1/{id}':", "-  /reports/{year}/summary:"),
		fp("routes/api.ts", "@@ -1,1 +1,0 @@", "-  \"/invoices/{id}\": handler,"),
	}
	got := Surface(files, surfaceFilter(t), 50)
	// Gone in diff order across the files, then the routes; "summary" is a
	// stop word, and routes/api.ts is no API description.
	want := ids("gone", "reports", "gone", "year", "gone", "invoices", "gone", "handler", "route", "/reports")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Surface = %v\nwant      %v", got, want)
	}
	for _, id := range got {
		if id.Word() != (id.Kind != KindRoute) {
			t.Errorf("%v: Word() = %v", id, id.Word())
		}
	}
}

// TestSurfaceOrderCapAndCount: gone, routes, changed, symbols; a name is kept
// once with its first kind; the cap keeps the first max and Derive counts
// what it drops; max <= 0 is related.max_identifiers' default; a
// related.contract_globs glob makes a file a contract file.
func TestSurfaceOrderCapAndCount(t *testing.T) {
	files := []provider.FilePatch{
		fp("defs/billing.idl",
			"@@ -1,7 +1,2 @@",
			"-  field alpha_one;",
			"-  field beta_two;",
			"+  field beta_two_renamed;",
			"-  field gamma_three amount;",
			"+  field gamma_three total;",
			"-  field delta_four;",
			"-  field alpha_one;"),
		fp("swagger.yaml",
			"@@ -1,2 +1,2 @@",
			"-  /ledger/{id}:",
			"+  /ledger/{id}/v2:"),
	}
	flt := surfaceFilter(t, "*.idl")
	all, dropped := Derive(files, flt, 50)
	want := ids("gone", "alpha_one", "gone", "beta_two", "gone", "amount", "gone", "delta_four",
		"route", "/ledger", "changed", "gamma_three", "changed", "ledger")
	if !reflect.DeepEqual(all, want) || dropped != 0 {
		t.Fatalf("Derive = %v, %d\nwant     %v, 0", all, dropped, want)
	}
	got, dropped := Derive(files, flt, 3)
	if !reflect.DeepEqual(got, want[:3]) || dropped != len(want)-3 {
		t.Errorf("Derive(3) = %v, %d; want %v, %d", got, dropped, want[:3], len(want)-3)
	}
	if got := Surface(files, flt, 3); !reflect.DeepEqual(got, want[:3]) {
		t.Errorf("Surface(3) = %v", got)
	}
	if none := Surface(files, surfaceFilter(t), 50); !reflect.DeepEqual(none, ids("route", "/ledger", "changed", "ledger")) {
		// Without the glob only the API description counts: its route, and
		// "ledger", which is on the added line of the same hunk.
		t.Errorf("Surface without the glob = %v", none)
	}
	var many []string
	many = append(many, "@@ -1,30 +1,0 @@")
	for i := 0; i < 30; i++ {
		many = append(many, "-  field_"+strings.Repeat("x", i+1)+";")
	}
	got, dropped = Derive([]provider.FilePatch{fp("a.proto", many...)}, nil, 0)
	if len(got) != config.DefaultRelatedIdentifiers || dropped != 30-config.DefaultRelatedIdentifiers {
		t.Errorf("Derive(0) kept %d, dropped %d", len(got), dropped)
	}
}

// TestSurfaceIgnoresMovedLinesAndMalformed: a token whose removed line comes
// back in another hunk of the same file is neither gone nor changed, and a
// patch that cannot be parsed gives nothing.
func TestSurfaceIgnoresMovedLinesAndMalformed(t *testing.T) {
	files := []provider.FilePatch{
		fp("a.proto", "@@ -1,2 +1,1 @@", "-  string moved_field = 1;", " }", "@@ -9,1 +9,2 @@", " message B {", "+  string moved_field = 1;"),
		{Path: "b.proto", Patch: "not a patch\n-  string lost = 1;\n"},
		{Path: "c.proto", Binary: true},
	}
	if got := Surface(files, nil, 50); len(got) != 0 {
		t.Errorf("Surface = %v, want none", got)
	}
}

func TestStopped(t *testing.T) {
	for _, w := range []string{"string", "Int32", "operationId", "DESCRIPTION", "proto3", "required", "minOccurs"} {
		if !Stopped(w) {
			t.Errorf("%q is not stopped", w)
		}
	}
	for _, w := range []string{"userName", "legacy_email", "amountDue"} {
		if Stopped(w) {
			t.Errorf("%q is stopped", w)
		}
	}
}

// TestStoppedRemovals: a removed name that only the stop list kept out of
// the surface is named (V-13b spec §4 item 1a); a name that is searched, a
// stop-listed name that is only changed, and a file that is no contract
// file are not.
func TestStoppedRemovals(t *testing.T) {
	flt := surfaceFilter(t)
	files := []provider.FilePatch{
		fp("api/openapi.yaml",
			"@@ -10,7 +10,6 @@ components:",
			"     User:",
			"       properties:",
			"-        name:",
			"-        userName:",
			"-        type:",
			"+        displayName:",
			"         id:",
			"-        name:"),
		fp("src/app.go", "@@ -1,2 +1,1 @@", "-var description = 1", "+var x = 1"),
	}
	if got := StoppedRemovals(files, flt); !slices.Equal(got, []string{"name", "type"}) {
		t.Errorf("StoppedRemovals = %v, want [name type]", got)
	}
	if ids := Surface(files, flt, 50); len(ids) != 1 || ids[0].Text != "userName" {
		t.Errorf("the stop-listed names are searched: %v", ids)
	}
	if got := StoppedRemovals(nil, flt); got != nil {
		t.Errorf("StoppedRemovals(nil) = %v", got)
	}
}
