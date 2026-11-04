package catalog_test

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/catalog"
)

func TestPricingUsesCoreUsageAdmissionAndPreservesPresence(t *testing.T) {
	pricing := catalog.Pricing{InputPer1M: 2, CacheReadPer1M: new(0.5), CacheWritePer1M: new(3.0)}
	for _, usage := range []chat.Usage{
		{InputTokens: 10, CacheReadInputTokens: new(int64(8)), CacheWriteInputTokens: new(int64(8))},
		{InputTokens: math.MaxInt64, OutputTokens: 1},
		{InputTokens: math.MaxInt64, CacheReadInputTokens: new(int64(math.MaxInt64)), CacheWriteInputTokens: new(int64(1))},
	} {
		for _, cost := range []func(chat.Usage) (float64, error){pricing.Cost, (catalog.PricingSchedule{pricing}).Cost} {
			if value, err := cost(usage); value != 0 || !errors.Is(err, chat.ErrInvalidUsage) {
				t.Errorf("pricing bypassed Core admission: value=%g error=%v", value, err)
			}
		}
	}
	for _, test := range []struct {
		usage chat.Usage
		want  float64
	}{
		{usage: chat.Usage{InputTokens: 1000}, want: 0.002},
		{usage: chat.Usage{InputTokens: 1000, CacheReadInputTokens: new(int64(0)), CacheWriteInputTokens: new(int64(0))}, want: 0.002},
		{usage: chat.Usage{InputTokens: 1000, CacheReadInputTokens: new(int64(400))}, want: 0.0014},
	} {
		before := test.usage.Clone()
		if cost, err := pricing.Cost(test.usage); err != nil || !approximately(cost, test.want) {
			t.Fatalf("cost=%g error=%v, want %g", cost, err, test.want)
		}
		if !reflect.DeepEqual(test.usage, before) {
			t.Fatal("pricing changed reported usage presence")
		}
	}
}
