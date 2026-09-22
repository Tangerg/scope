package catalog

import "slices"

const tokensPerMillion = 1_000_000

// Pricing is one rate-card band in USD per one million tokens. Threshold is
// the input-token count at which the band reprices the whole call.
type Pricing struct {
	Threshold       int64   `json:"threshold,omitzero"`
	InputPer1M      float64 `json:"input_per_1m"`
	OutputPer1M     float64 `json:"output_per_1m"`
	CacheReadPer1M  float64 `json:"cache_read_per_1m,omitzero"`
	CacheWritePer1M float64 `json:"cache_write_per_1m,omitzero"`
}

func (p Pricing) IsZero() bool { return p == Pricing{} }

// PricingSchedule is a threshold-ordered rate card. Each applicable threshold
// reprices the complete call rather than only the tokens above that threshold.
type PricingSchedule []Pricing

func (p PricingSchedule) Clone() PricingSchedule {
	return slices.Clone(p)
}

// Usage is the provider-neutral token breakdown required for pricing.
type Usage struct {
	InputTokens           int64
	OutputTokens          int64
	CacheReadInputTokens  int64
	CacheWriteInputTokens int64
}

// Cost selects the highest applicable pricing band and computes USD cost.
func (p PricingSchedule) Cost(usage Usage) float64 {
	if len(p) == 0 {
		return 0
	}
	band := p[0]
	for _, candidate := range slices.Backward(p) {
		if usage.InputTokens >= candidate.Threshold {
			band = candidate
			break
		}
	}
	return band.Cost(usage)
}

// Cost computes USD cost using this pricing band.
func (p Pricing) Cost(usage Usage) float64 {
	if p.IsZero() {
		return 0
	}
	cacheRead := max(usage.CacheReadInputTokens, 0)
	cacheWrite := max(usage.CacheWriteInputTokens, 0)
	uncachedInput := max(usage.InputTokens-cacheRead-cacheWrite, 0)

	readRate := p.CacheReadPer1M
	if readRate == 0 {
		readRate = p.InputPer1M
	}
	writeRate := p.CacheWritePer1M
	if writeRate == 0 {
		writeRate = p.InputPer1M
	}

	total := float64(uncachedInput)*p.InputPer1M +
		float64(usage.OutputTokens)*p.OutputPer1M +
		float64(cacheRead)*readRate +
		float64(cacheWrite)*writeRate
	return total / tokensPerMillion
}
