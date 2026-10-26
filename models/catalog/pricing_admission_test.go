package catalog_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/Tangerg/scope/models/catalog"
)

func TestPricingPreservesExplicitFreeCacheRates(t *testing.T) {
	for _, field := range []string{"cache_read_per_1m", "cache_write_per_1m"} {
		t.Run(field, func(t *testing.T) {
			var pricing catalog.Pricing
			if err := jsonv2.Unmarshal([]byte(`{"input_per_1m":2,"output_per_1m":8,"`+field+`":0}`), &pricing); err != nil {
				t.Fatal(err)
			}
			usage := catalog.Usage{InputTokens: 100}
			if field == "cache_read_per_1m" {
				usage.CacheReadInputTokens = usage.InputTokens
			} else {
				usage.CacheWriteInputTokens = usage.InputTokens
			}
			if cost, err := pricing.Cost(usage); err != nil || cost != 0 {
				t.Fatalf("explicitly free cache cost = %g", cost)
			}
		})
	}
}

func TestPricingRejectsNegativeOutputAndOverflow(t *testing.T) {
	if cost, err := (catalog.Pricing{OutputPer1M: 1}).Cost(catalog.Usage{OutputTokens: -1}); err == nil || cost != 0 {
		t.Errorf("negative tokens became a negative bill: %g", cost)
	}
	if cost, err := (catalog.Pricing{OutputPer1M: math.MaxFloat64}).Cost(catalog.Usage{OutputTokens: math.MaxInt64}); err == nil || cost != 0 {
		t.Errorf("finite rate and count overflowed to a non-finite bill: %g", cost)
	}
}

func TestPricingRejectsInvalidUsageWithoutInventingACost(t *testing.T) {
	for _, usage := range []catalog.Usage{
		{InputTokens: -1}, {OutputTokens: -1}, {CacheReadInputTokens: -1}, {CacheWriteInputTokens: -1},
		{InputTokens: 1, CacheReadInputTokens: 2}, {InputTokens: 1, CacheWriteInputTokens: 2},
		{InputTokens: 2, CacheReadInputTokens: 2, CacheWriteInputTokens: 1},
		{InputTokens: math.MaxInt64, CacheReadInputTokens: math.MaxInt64, CacheWriteInputTokens: math.MaxInt64},
	} {
		if usage.Validate() == nil {
			t.Errorf("invalid usage admitted: %+v", usage)
		}
		pricing := catalog.Pricing{InputPer1M: 1, OutputPer1M: 2}
		if cost, err := pricing.Cost(usage); err == nil || cost != 0 {
			t.Errorf("pricing admitted %+v: cost=%g error=%v", usage, cost, err)
		}
		if cost, err := (catalog.PricingSchedule{pricing}).Cost(usage); err == nil || cost != 0 {
			t.Errorf("schedule admitted %+v: cost=%g error=%v", usage, cost, err)
		}
	}
}

func TestPricingRejectsInvalidRatesAndSchedules(t *testing.T) {
	for _, pricing := range []catalog.Pricing{
		{Threshold: -1}, {InputPer1M: -1}, {InputPer1M: math.NaN()}, {OutputPer1M: math.Inf(1)},
		{CacheReadPer1M: new(-1.0)}, {CacheWritePer1M: new(math.NaN())}, {CacheReadPer1M: new(math.Inf(-1))},
	} {
		if pricing.Validate() == nil {
			t.Errorf("invalid rates admitted: %+v", pricing)
		}
		if cost, err := pricing.Cost(catalog.Usage{}); err == nil || cost != 0 {
			t.Errorf("pricing cost=%g error=%v for %+v", cost, err, pricing)
		}
		if cost, err := (catalog.PricingSchedule{pricing}).Cost(catalog.Usage{}); err == nil || cost != 0 {
			t.Errorf("schedule cost=%g error=%v for %+v", cost, err, pricing)
		}
	}
	for _, schedule := range []catalog.PricingSchedule{
		{{Threshold: 1}}, {{}, {}}, {{}, {Threshold: 10}, {Threshold: 5}},
	} {
		if schedule.Validate() == nil {
			t.Errorf("invalid schedule admitted: %+v", schedule)
		}
	}
	pricing := catalog.Pricing{InputPer1M: math.MaxFloat64, OutputPer1M: math.MaxFloat64}
	if cost, err := pricing.Cost(catalog.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}); err == nil || cost != 0 {
		t.Fatalf("sum of finite component costs overflowed: cost=%g error=%v", cost, err)
	}
	if cost, err := pricing.Cost(catalog.Usage{InputTokens: 1_000_000}); err != nil || cost != math.MaxFloat64 {
		t.Fatalf("representable cost rejected because of intermediate multiplication: cost=%g error=%v", cost, err)
	}
}

func TestPricingPresenceSurvivesSerializationAndClone(t *testing.T) {
	for _, raw := range []string{
		`{"input_per_1m":0,"output_per_1m":0}`,
		`{"input_per_1m":1,"output_per_1m":2,"cache_read_per_1m":0}`,
		`{"input_per_1m":1,"output_per_1m":2,"cache_write_per_1m":0}`,
		`{"input_per_1m":1,"output_per_1m":2,"cache_read_per_1m":0.25,"cache_write_per_1m":0.5}`,
	} {
		var original catalog.Pricing
		if err := jsonv2.Unmarshal([]byte(raw), &original); err != nil {
			t.Fatal(err)
		}
		encoded, err := jsonv2.Marshal(original)
		if err != nil || string(encoded) != raw {
			t.Fatalf("pricing presence changed: got=%s want=%s error=%v", encoded, raw, err)
		}
		clone := (catalog.Model{ID: "model", Pricing: catalog.PricingSchedule{original}}).Clone()
		if clone.Pricing[0].CacheReadPer1M != nil {
			*clone.Pricing[0].CacheReadPer1M = 99
			if *original.CacheReadPer1M == 99 {
				t.Fatal("cache read rate ownership escaped the catalog clone")
			}
		}
		if clone.Pricing[0].CacheWritePer1M != nil {
			*clone.Pricing[0].CacheWritePer1M = 99
			if *original.CacheWritePer1M == 99 {
				t.Fatal("cache write rate ownership escaped the catalog clone")
			}
		}
	}
	usage := catalog.Usage{InputTokens: 100, OutputTokens: 100}
	if cost, err := (catalog.PricingSchedule{{}}).Cost(usage); err != nil || cost != 0 {
		t.Fatalf("known free schedule: cost=%g error=%v", cost, err)
	}
	if cost, err := (catalog.PricingSchedule{}).Cost(usage); !errors.Is(err, catalog.ErrUnknownPricing) || cost != 0 {
		t.Fatalf("unknown schedule: cost=%g error=%v", cost, err)
	}
}

func TestPricingRejectsMalformedDataWithoutReplacingTheReceiver(t *testing.T) {
	for _, raw := range []string{"null", "[]", "{}", `{"input_per_1m":0}`, `{"input_per_1m":null,"output_per_1m":0}`, `{"input_per_1m":-1,"output_per_1m":0}`, `{"input_per_1m":0,"output_per_1m":0,"extra":1}`} {
		pricing := catalog.Pricing{InputPer1M: 5, OutputPer1M: 7}
		if err := jsonv2.Unmarshal([]byte(raw), &pricing); err == nil {
			t.Errorf("invalid pricing admitted: %s", raw)
		}
		if pricing.InputPer1M != 5 || pricing.OutputPer1M != 7 {
			t.Errorf("failed decode replaced pricing: %+v", pricing)
		}
	}
	var absent catalog.Pricing
	if err := jsonv2.Unmarshal([]byte(`{"input_per_1m":1,"output_per_1m":2,"cache_read_per_1m":null}`), &absent); err != nil {
		t.Fatal(err)
	}
	encoded, err := jsonv2.Marshal(absent)
	if err != nil || strings.Contains(string(encoded), "cache_read_per_1m") {
		t.Fatalf("absent cache rate became a price: %s, %v", encoded, err)
	}
}
