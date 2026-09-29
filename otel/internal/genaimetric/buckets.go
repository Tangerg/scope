// Package genaimetric owns the histogram boundaries that the GenAI semantic
// conventions recommend, so every adapter reports comparable distributions.
package genaimetric

import "go.opentelemetry.io/otel/metric"

// DurationBuckets are the recommended boundaries, in seconds, for GenAI
// operation durations and chunk latencies.
func DurationBuckets() metric.HistogramOption {
	return metric.WithExplicitBucketBoundaries(
		0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92,
	)
}

// TokenBuckets are the recommended boundaries for gen_ai.client.token.usage.
func TokenBuckets() metric.HistogramOption {
	return metric.WithExplicitBucketBoundaries(
		1, 4, 16, 64, 256, 1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216, 67108864,
	)
}
