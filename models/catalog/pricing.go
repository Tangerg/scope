package catalog

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"slices"
)

const tokensPerMillion = 1_000_000

var ErrUnknownPricing = errors.New("catalog: pricing is unknown")

// Pricing is one rate-card band in USD per one million tokens. Threshold is
// the input-token count that must be exceeded before the band reprices the
// whole call. The first band is the base rate, including zero-token calls.
type Pricing struct {
	Threshold   int64   `json:"threshold,omitzero"`
	InputPer1M  float64 `json:"input_per_1m"`
	OutputPer1M float64 `json:"output_per_1m"`
	// Missing cache rates use the input rate; a present zero rate is free.
	CacheReadPer1M  *float64 `json:"cache_read_per_1m,omitzero"`
	CacheWritePer1M *float64 `json:"cache_write_per_1m,omitzero"`
}

func (p *Pricing) UnmarshalJSON(data []byte) error {
	type wirePricing struct {
		Threshold       int64    `json:"threshold,omitzero"`
		InputPer1M      *float64 `json:"input_per_1m"`
		OutputPer1M     *float64 `json:"output_per_1m"`
		CacheReadPer1M  *float64 `json:"cache_read_per_1m,omitzero"`
		CacheWritePer1M *float64 `json:"cache_write_per_1m,omitzero"`
	}
	var wire *wirePricing
	if err := jsonv2.Unmarshal(data, &wire, jsonv2.RejectUnknownMembers(true)); err != nil {
		return err
	}
	if wire == nil || wire.InputPer1M == nil || wire.OutputPer1M == nil {
		return errors.New("catalog: pricing requires an object with input and output rates")
	}
	decoded := Pricing{
		Threshold: wire.Threshold, InputPer1M: *wire.InputPer1M, OutputPer1M: *wire.OutputPer1M,
		CacheReadPer1M: wire.CacheReadPer1M, CacheWritePer1M: wire.CacheWritePer1M,
	}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*p = decoded
	return nil
}

func (p Pricing) IsZero() bool { return p == Pricing{} }

func (p Pricing) Clone() Pricing {
	if p.CacheReadPer1M != nil {
		p.CacheReadPer1M = new(*p.CacheReadPer1M)
	}
	if p.CacheWritePer1M != nil {
		p.CacheWritePer1M = new(*p.CacheWritePer1M)
	}
	return p
}

func (p Pricing) Validate() error {
	if p.Threshold < 0 {
		return errors.New("catalog: pricing threshold must not be negative")
	}
	for _, rate := range []float64{p.InputPer1M, p.OutputPer1M} {
		if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
			return errors.New("catalog: token rates must be finite and non-negative")
		}
	}
	for _, rate := range []*float64{p.CacheReadPer1M, p.CacheWritePer1M} {
		if rate != nil && (math.IsNaN(*rate) || math.IsInf(*rate, 0) || *rate < 0) {
			return errors.New("catalog: cache rates must be finite and non-negative")
		}
	}
	return nil
}

func (p Pricing) Cost(usage Usage) (float64, error) {
	if err := p.Validate(); err != nil {
		return 0, err
	}
	if err := usage.Validate(); err != nil {
		return 0, err
	}
	return p.calculate(usage)
}

func (p Pricing) calculate(usage Usage) (float64, error) {
	uncachedInput := usage.InputTokens - usage.CacheReadInputTokens - usage.CacheWriteInputTokens
	readRate, writeRate := p.InputPer1M, p.InputPer1M
	if p.CacheReadPer1M != nil {
		readRate = *p.CacheReadPer1M
	}
	if p.CacheWritePer1M != nil {
		writeRate = *p.CacheWritePer1M
	}
	total := float64(uncachedInput)/tokensPerMillion*p.InputPer1M +
		float64(usage.OutputTokens)/tokensPerMillion*p.OutputPer1M +
		float64(usage.CacheReadInputTokens)/tokensPerMillion*readRate +
		float64(usage.CacheWriteInputTokens)/tokensPerMillion*writeRate
	if math.IsInf(total, 0) {
		return 0, errors.New("catalog: token cost exceeds float64 range")
	}
	return total, nil
}

// PricingSchedule is a threshold-ordered rate card. Each applicable threshold
// reprices the complete call rather than only the tokens above that threshold.
type PricingSchedule []Pricing

func (p PricingSchedule) Clone() PricingSchedule {
	cloned := slices.Clone(p)
	for index, band := range cloned {
		cloned[index] = band.Clone()
	}
	return cloned
}

func (p PricingSchedule) Validate() error {
	for index, band := range p {
		if err := band.Validate(); err != nil {
			return fmt.Errorf("catalog: pricing band[%d]: %w", index, err)
		}
		if index == 0 && band.Threshold != 0 {
			return errors.New("catalog: pricing schedule requires a base band with threshold zero")
		}
		if index > 0 && band.Threshold <= p[index-1].Threshold {
			return errors.New("catalog: pricing thresholds must be strictly increasing")
		}
	}
	return nil
}

// Cache counts partition InputTokens, including cache writes whose provider
// response may originally report them outside its uncached input count.
type Usage struct {
	InputTokens           int64
	OutputTokens          int64
	CacheReadInputTokens  int64
	CacheWriteInputTokens int64
}

func (u Usage) Validate() error {
	if u.InputTokens < 0 || u.OutputTokens < 0 || u.CacheReadInputTokens < 0 || u.CacheWriteInputTokens < 0 {
		return errors.New("catalog: token counts must not be negative")
	}
	if u.CacheReadInputTokens > u.InputTokens || u.CacheWriteInputTokens > u.InputTokens-u.CacheReadInputTokens {
		return errors.New("catalog: cache token counts must not exceed total input tokens")
	}
	return nil
}

// An empty schedule means unknown pricing; one zero-rate band means free.
func (p PricingSchedule) Cost(usage Usage) (float64, error) {
	if err := p.Validate(); err != nil {
		return 0, err
	}
	if err := usage.Validate(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, ErrUnknownPricing
	}
	band := p[0]
	for _, candidate := range slices.Backward(p) {
		if usage.InputTokens > candidate.Threshold {
			band = candidate
			break
		}
	}
	return band.calculate(usage)
}
