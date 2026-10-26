package catalog_test

import (
	"errors"
	"math"
	"testing"

	"github.com/Tangerg/scope/models/catalog"
)

func approximately(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestPricingCost(t *testing.T) {
	pricing := catalog.Pricing{InputPer1M: 3, OutputPer1M: 15, CacheReadPer1M: new(0.3), CacheWritePer1M: new(3.75)}
	cases := []struct {
		name  string
		usage catalog.Usage
		want  float64
	}{
		{name: "no cache", usage: catalog.Usage{InputTokens: 1000, OutputTokens: 500}, want: 0.0105},
		{
			name: "cache read and write",
			usage: catalog.Usage{
				InputTokens: 1000, OutputTokens: 500,
				CacheReadInputTokens: 800, CacheWriteInputTokens: 100,
			},
			want: 0.008415,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got, err := pricing.Cost(test.usage); err != nil || !approximately(got, test.want) {
				t.Fatalf("Cost = %v, want %v", got, test.want)
			}
		})
	}
	if got, err := (catalog.Pricing{}).Cost(catalog.Usage{InputTokens: 1000}); err != nil || got != 0 {
		t.Fatalf("zero Pricing cost = %v", got)
	}
}

func TestPricingCacheFallbackAndBands(t *testing.T) {
	pricing := catalog.Pricing{InputPer1M: 2, OutputPer1M: 8}
	usage := catalog.Usage{InputTokens: 1000, CacheReadInputTokens: 400}
	if got, err := pricing.Cost(usage); err != nil || !approximately(got, 0.002) {
		t.Fatalf("cache fallback cost = %v", got)
	}

	bands := catalog.PricingSchedule{
		{InputPer1M: 1.25, OutputPer1M: 10},
		{Threshold: 200_000, InputPer1M: 2.5, OutputPer1M: 15},
	}
	below, belowErr := bands.Cost(catalog.Usage{InputTokens: 100_000, OutputTokens: 10_000})
	above, aboveErr := bands.Cost(catalog.Usage{InputTokens: 250_000, OutputTokens: 10_000})
	if belowErr != nil || aboveErr != nil || !approximately(below, 0.225) || !approximately(above, 0.775) {
		t.Fatalf("tier costs = %v, %v", below, above)
	}
	if got, err := (catalog.PricingSchedule{}).Cost(catalog.Usage{InputTokens: 1000}); !errors.Is(err, catalog.ErrUnknownPricing) || got != 0 {
		t.Fatalf("empty schedule cost = %v", got)
	}
}

func TestZeroValueReporting(t *testing.T) {
	if !(catalog.Model{}).IsZero() || !(catalog.Limits{}).IsZero() || !(catalog.Modalities{}).IsZero() {
		t.Fatal("a zero value reported non-zero")
	}
	if (catalog.Model{ID: "x"}).IsZero() {
		t.Fatal("Model with an ID reported zero")
	}
	if (catalog.Model{Limits: catalog.Limits{ContextWindow: 1}}).IsZero() {
		t.Fatal("Model carrying limits reported zero")
	}
	if (catalog.Model{Modalities: catalog.Modalities{Input: []catalog.Modality{catalog.ModalityText}}}).IsZero() {
		t.Fatal("Model carrying modalities reported zero")
	}
	if (catalog.Limits{MaxOutputTokens: 1}).IsZero() {
		t.Fatal("populated Limits reported zero")
	}
	if (catalog.Modalities{Output: []catalog.Modality{catalog.ModalityText}}).IsZero() {
		t.Fatal("populated Modalities reported zero")
	}
}

func TestCapabilities(t *testing.T) {
	modalities := catalog.Modalities{
		Input:  []catalog.Modality{catalog.ModalityText, catalog.ModalityImage, catalog.ModalityPDF},
		Output: []catalog.Modality{catalog.ModalityText},
	}
	if !modalities.AcceptsInput(catalog.ModalityImage) || modalities.AcceptsInput(catalog.ModalityAudio) {
		t.Fatal("AcceptsInput mismatch")
	}
	if !modalities.EmitsOutput(catalog.ModalityText) || modalities.EmitsOutput(catalog.ModalityImage) {
		t.Fatal("EmitsOutput mismatch")
	}
	if (catalog.Reasoning{Supported: true}).IsZero() {
		t.Fatal("supported reasoning reported zero")
	}
}
