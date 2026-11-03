package tool

// Effect is a replay promise, independent of concurrency and the outcome of a
// particular call. Idempotent requires durable backend deduplication under the
// same nonzero IdempotencyKey, including intervening calls; assigning a value
// twice is not sufficient. Safe calls request no mutation of application state;
// incidental accounting and telemetry do not authorize repeated business effects.
type Effect string

const (
	EffectUnsafe     Effect = ""
	EffectIdempotent Effect = "idempotent"
	EffectSafe       Effect = "safe"
)

func (e Effect) Valid() bool { return e == EffectUnsafe || e == EffectIdempotent || e == EffectSafe }

func (e Effect) Replayable() bool { return e == EffectIdempotent || e == EffectSafe }
