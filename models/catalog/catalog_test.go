package catalog_test

import (
	"testing"

	"github.com/Tangerg/scope/models/catalog"
)

func TestModels(t *testing.T) {
	models := catalog.Default.Models("anthropic")
	if len(models) == 0 {
		t.Fatal("anthropic should have cataloged models")
	}
	// Case-insensitive on the provider name (adapter consts are capitalized).
	if len(catalog.Default.Models("Anthropic")) != len(models) {
		t.Fatal("provider name must match case-insensitively")
	}
	if catalog.Default.Models("does-not-exist") != nil {
		t.Fatal("unknown provider must return nil")
	}
}

func TestLookup(t *testing.T) {
	info, ok := catalog.Default.Lookup("anthropic", "claude-opus-5")
	if !ok {
		t.Fatal("known model must be found")
	}
	if len(info.Pricing) == 0 {
		t.Fatal("model info must carry pricing")
	}
	if _, ok := catalog.Default.Lookup("anthropic", "no-such-model"); ok {
		t.Fatal("unknown model must report ok=false")
	}
	if _, ok := catalog.Default.Lookup("does-not-exist", "claude-opus-5"); ok {
		t.Fatal("unknown provider must report ok=false")
	}
}

func TestProvider(t *testing.T) {
	p, ok := catalog.Default.Provider("anthropic")
	if !ok || p.ID != "anthropic" || len(p.Models) == 0 || p.Models[0].ID == "" {
		t.Fatalf("Provider(anthropic) = %+v, %v", p, ok)
	}
	if _, ok := catalog.Default.Provider("does-not-exist"); ok {
		t.Fatal("Provider of unknown provider must report ok=false")
	}
}
