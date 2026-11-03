package catalog_test

import (
	"testing"

	"github.com/Tangerg/scope/models/catalog"
)

func TestModels(t *testing.T) {
	models := catalog.Models("anthropic")
	if len(models) == 0 {
		t.Fatal("anthropic should have cataloged models")
	}
	if catalog.Models("Anthropic") != nil {
		t.Fatal("provider identity matched another spelling")
	}
	if catalog.Models("does-not-exist") != nil {
		t.Fatal("unknown provider must return nil")
	}
}

func TestLookup(t *testing.T) {
	info, ok := catalog.LookupModel("anthropic", "claude-opus-5")
	if !ok {
		t.Fatal("known model must be found")
	}
	if len(info.Pricing) == 0 {
		t.Fatal("model info must carry pricing")
	}
	if _, ok := catalog.LookupModel("anthropic", "no-such-model"); ok {
		t.Fatal("unknown model must report ok=false")
	}
	if _, ok := catalog.LookupModel("does-not-exist", "claude-opus-5"); ok {
		t.Fatal("unknown provider must report ok=false")
	}
}

func TestProvider(t *testing.T) {
	p, ok := catalog.LookupProvider("anthropic")
	if !ok || p.ID != "anthropic" || len(p.Models) == 0 || p.Models[0].ID == "" {
		t.Fatalf("Provider(anthropic) = %+v, %v", p, ok)
	}
	if _, ok := catalog.LookupProvider("does-not-exist"); ok {
		t.Fatal("Provider of unknown provider must report ok=false")
	}
}

func TestCatalogDoesNotAdvertiseRetiredPerplexitySonar(t *testing.T) {
	for _, id := range []string{"sonar", "sonar-pro", "sonar-reasoning-pro", "sonar-deep-research"} {
		if _, found := catalog.LookupModel("perplexity", id); found {
			t.Fatalf("retired Sonar model %q was advertised for the Agent API", id)
		}
	}
}
