package catalog

import (
	"slices"
	"testing"
)

func TestCatalogIntegrity(t *testing.T) {
	if len(entries) == 0 {
		t.Fatal("catalog is empty")
	}
	for provider, entry := range entries {
		if len(entry.models) == 0 {
			t.Errorf("provider %q has no models", provider)
		}
		for id, model := range entry.models {
			if id == "" || model.ID == "" {
				t.Errorf("provider %q has a model with empty id", provider)
			}
			previous := int64(-1)
			for _, band := range model.Pricing {
				if band.InputPer1M <= 0 {
					t.Errorf("%s/%s: priced band threshold=%d has input rate %v", provider, id, band.Threshold, band.InputPer1M)
				}
				if band.Threshold <= previous {
					t.Errorf("%s/%s: pricing bands are not ascending", provider, id)
				}
				previous = band.Threshold
			}
			// Effort levels are hand-curated (the upstream source carries only
			// a reasoning bool), so a default the model does not offer is a
			// plausible slip and one a caller can only discover by being
			// rejected by the provider.
			if len(model.Reasoning.Levels) > 0 {
				if !model.Reasoning.Supported {
					t.Errorf("%s/%s: carries reasoning levels without reporting reasoning support", provider, id)
				}
				if !slices.Contains(model.Reasoning.Levels, model.Reasoning.DefaultLevel) {
					t.Errorf("%s/%s: default reasoning level %q is not among %v", provider, id, model.Reasoning.DefaultLevel, model.Reasoning.Levels)
				}
			}
		}
	}
}

func TestLookupReturnsOwnedSlices(t *testing.T) {
	first, ok := Default.Lookup("anthropic", "claude-opus-5")
	if !ok || len(first.Pricing) == 0 {
		t.Fatal("fixture missing")
	}
	first.Pricing[0].InputPer1M = -1
	second, _ := Default.Lookup("anthropic", "claude-opus-5")
	if second.Pricing[0].InputPer1M == -1 {
		t.Fatal("Lookup returned catalog-owned pricing slice")
	}
}
