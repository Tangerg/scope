package catalog_test

import (
	"testing"

	"github.com/Tangerg/scope/models/catalog"
)

func TestPricingThresholdIsExclusive(t *testing.T) {
	schedule := catalog.PricingSchedule{
		{InputPer1M: 1, OutputPer1M: 2, CacheReadPer1M: 0.25, CacheWritePer1M: 1.25},
		{Threshold: 200000, InputPer1M: 3, OutputPer1M: 4, CacheReadPer1M: 0.5, CacheWritePer1M: 3.75},
	}
	for _, test := range []struct {
		tokens int64
		want   float64
	}{
		{199999, 0.401499}, {200000, 0.4015}, {200001, 1.004253},
	} {
		usage := catalog.Usage{InputTokens: test.tokens, OutputTokens: 100000, CacheReadInputTokens: 1000, CacheWriteInputTokens: 9000}
		if got := schedule.Cost(usage); !approximately(got, test.want) {
			t.Errorf("tokens=%d cost=%g want=%g", test.tokens, got, test.want)
		}
	}
}
func TestGeminiPricingBoundary(t *testing.T) {
	model, ok := catalog.Default.Lookup("google", "gemini-2.5-pro")
	if !ok {
		t.Fatal("model missing")
	}
	for _, test := range []struct {
		tokens int64
		want   float64
	}{{199999, 0.24999875}, {200000, 0.25}, {200001, 0.5000025}} {
		if got := model.Pricing.Cost(catalog.Usage{InputTokens: test.tokens}); !approximately(got, test.want) {
			t.Errorf("tokens=%d cost=%g want=%g", test.tokens, got, test.want)
		}
	}
}
