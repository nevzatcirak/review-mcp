package filter

import (
	"slices"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

func TestContractTier(t *testing.T) {
	cases := map[string]int{
		// Tier 1: API descriptions by extension.
		"proto/user.proto": TierDescription, "api/schema.graphql": TierDescription, "q/Query.GQL": TierDescription,
		"events/order.avsc": TierDescription, "idl/service.thrift": TierDescription, "soap/legacy.wsdl": TierDescription,
		// Tier 1: by name, in any case.
		"openapi.yaml": TierDescription, "spec/OpenAPI-v2.yml": TierDescription, "docs/swagger.json": TierDescription,
		"asyncapi.events.yaml": TierDescription, "schemas/order.schema.json": TierDescription,
		"Config/User.Schema.JSON": TierDescription,
		// Not tier 1: the name without its extension, or another extension.
		"openapi.md": TierOther, "swagger.txt": TierOther, "openapi_test.go": TierOther,
		// Tier 2: API surface by a path token.
		"src/api/users.go":          TierSurface,
		"internal/routes/index.ts":  TierSurface,
		"app/controllers/home.rb":   TierSurface,
		"src/UserController.java":   TierSurface,
		"pkg/http/order_handler.go": TierSurface,
		"web/APIClient.ts":          TierSurface,
		"models/user-dto.ts":        TierSurface,
		"src/userDto.kt":            TierSurface,
		"lib/contracts/v1.go":       TierSurface,
		"svc/endpoints.py":          TierSurface,
		"internal/order/schemas.go": TierSurface,
		"app/Http/Router.php":       TierSurface,
		"client/index.js":           TierSurface,
		"src/RouteTable.go":         TierSurface,
		"src/OrderModels.java":      TierSurface,
		// Not tier 2: documentation, and words that only contain a token.
		"docs/api/users.md":       TierOther,
		"api/README":              TierOther,
		"api/CHANGELOG.rst":       TierOther,
		"src/rapid.go":            TierOther,
		"src/modeling/fit.py":     TierOther,
		"src/clientele/shop.go":   TierOther,
		"src/handlerless/main.go": TierOther,
		"src/app.go":              TierOther,
		"":                        TierOther,
	}
	for p, want := range cases {
		if got := ContractTier(p); got != want {
			t.Errorf("ContractTier(%q) = %d, want %d", p, got, want)
		}
	}
}

func TestContractTokens(t *testing.T) {
	cases := map[string][]string{
		"web/APIClient.ts":            {"web", "api", "client", "ts"},
		"src/UserController.java":     {"src", "user", "controller", "java"},
		"a/b_c-d.e":                   {"a", "b", "c", "d", "e"},
		"src/userDTOMapper.go":        {"src", "user", "dto", "mapper", "go"},
		"src/v2Routes.go":             {"src", "v2", "routes", "go"},
		`win\path\ApiHandler.cs`:      {"win", "path", "api", "handler", "cs"},
		"ALLCAPS/HTTPServer":          {"allcaps", "http", "server"},
		"src/already-lower_case.file": {"src", "already", "lower", "case", "file"},
	}
	for p, want := range cases {
		if got := contractTokens(p); !slices.Equal(got, want) {
			t.Errorf("contractTokens(%q) = %q, want %q", p, got, want)
		}
	}
}

// TestFilterContractGlobs: related.contract_globs make a path tier 1, with
// the matching rules of ignore.glob; an invalid glob fails New.
func TestFilterContractGlobs(t *testing.T) {
	cfg := config.Defaults()
	cfg.Related.ContractGlobs = []string{"*.messages.ts", "shared/types/**"}
	f, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]int{
		"web/src/order.messages.ts": TierDescription,
		"shared/types/order.ts":     TierDescription,
		"shared/types/deep/x.go":    TierDescription,
		"web/shared/types/x.ts":     TierOther, // "shared/types/**" is anchored at the root
		"src/api/x.go":              TierSurface,
		"proto/x.proto":             TierDescription,
	} {
		if got := f.ContractTier(p); got != want {
			t.Errorf("ContractTier(%q) = %d, want %d", p, got, want)
		}
	}
	var nilFilter *Filter
	if got := nilFilter.ContractTier("api/x.go"); got != TierSurface {
		t.Errorf("a nil filter: %d", got)
	}
	cfg.Related.ContractGlobs = []string{"[bad"}
	if _, err := New(cfg); err == nil {
		t.Error("an invalid related.contract_globs pattern was accepted")
	}
}
