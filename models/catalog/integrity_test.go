package catalog

import "testing"

func TestCatalogIntegrity(t *testing.T) {
	entries := loadEntries()
	if len(entries) == 0 {
		t.Fatal("catalog is empty")
	}
	for provider, entry := range entries {
		if len(entry.models) == 0 {
			t.Errorf("provider %q has no models", provider)
		}
		for id, model := range entry.models {
			if err := model.Validate(); err != nil {
				t.Errorf("%s/%s: %v", provider, id, err)
			}
		}
	}
}

func TestLookupReturnsOwnedSlices(t *testing.T) {
	first, ok := LookupModel("anthropic", "claude-opus-5")
	if !ok || len(first.Pricing) == 0 {
		t.Fatal("fixture missing")
	}
	first.Pricing[0].InputPer1M = -1
	second, _ := LookupModel("anthropic", "claude-opus-5")
	if second.Pricing[0].InputPer1M == -1 {
		t.Fatal("Lookup returned catalog-owned pricing slice")
	}
}
