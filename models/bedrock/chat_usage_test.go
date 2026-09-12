package bedrock

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	corechat "github.com/Tangerg/scope/core/chat"
)

// AWS states that with prompt caching "the inputTokens field represents only
// the non-cached input tokens", and gives the total as
// inputTokens + cacheReadInputTokens + cacheWriteInputTokens. Core reports that
// total with the cache counts as breakdowns of it.
func TestUsageTotalsTheDisjointInputCounters(t *testing.T) {
	t.Parallel()

	for _, sample := range []struct {
		name       string
		usage      *types.TokenUsage
		wantInput  int64
		wantOutput int64
	}{
		{
			name:      "no caching",
			usage:     &types.TokenUsage{InputTokens: aws.Int32(120), OutputTokens: aws.Int32(30)},
			wantInput: 120, wantOutput: 30,
		},
		{
			name: "cache hit dwarfs the uncached remainder",
			usage: &types.TokenUsage{
				InputTokens:          aws.Int32(5),
				OutputTokens:         aws.Int32(30),
				CacheReadInputTokens: aws.Int32(1920),
			},
			wantInput: 1925, wantOutput: 30,
		},
		{
			name: "cache write and read together",
			usage: &types.TokenUsage{
				InputTokens:           aws.Int32(5),
				OutputTokens:          aws.Int32(30),
				CacheReadInputTokens:  aws.Int32(1024),
				CacheWriteInputTokens: aws.Int32(512),
			},
			wantInput: 1541, wantOutput: 30,
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			usage := mapProtocolUsage(sample.usage)
			if usage.InputTokens != sample.wantInput {
				t.Fatalf("InputTokens = %d, want %d", usage.InputTokens, sample.wantInput)
			}
			if usage.OutputTokens != sample.wantOutput {
				t.Fatalf("OutputTokens = %d, want %d", usage.OutputTokens, sample.wantOutput)
			}
			// A breakdown above its own total is what Core rejects, and it is
			// exactly what copying inputTokens straight through produced.
			if err := usage.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

// A cache hit used to make the whole response invalid, because the breakdown
// exceeded the total it was supposed to be part of.
func TestUsageRejectedACacheHitBeforeNormalization(t *testing.T) {
	t.Parallel()

	raw := corechat.Usage{
		InputTokens:          5,
		OutputTokens:         30,
		CacheReadInputTokens: aws.Int64(1920),
	}
	if err := raw.Validate(); err == nil {
		t.Fatal("Validate() = nil for a breakdown above its total; the guard this fix relies on is gone")
	}
}

func TestUsageHandlesAMissingReport(t *testing.T) {
	t.Parallel()

	if usage := mapProtocolUsage(nil); usage != nil {
		t.Fatalf("mapProtocolUsage(nil) = %#v, want absent accounting", usage)
	}
}

func TestUsageRequiresBothReportedTotals(t *testing.T) {
	for _, report := range []*types.TokenUsage{{}, {InputTokens: aws.Int32(1)}, {OutputTokens: aws.Int32(1)}} {
		if usage := mapProtocolUsage(report); usage != nil {
			t.Fatalf("partial report = %#v, want absent accounting", usage)
		}
	}
	usage := mapProtocolUsage(&types.TokenUsage{InputTokens: aws.Int32(0), OutputTokens: aws.Int32(0)})
	if usage == nil || usage.InputTokens != 0 || usage.OutputTokens != 0 {
		t.Fatalf("explicit zero = %#v", usage)
	}
}
