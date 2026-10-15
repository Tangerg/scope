package agent

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

// Nearest-rank quantiles describe serial, closed-loop samples, not an arrival
// rate or a production SLO. Report the sample count even for bench smoke runs.
// Callers collect samples outside the allocation/timing window.
func reportBenchmarkLatencies(b *testing.B, name string, samples []time.Duration) {
	b.Helper()
	b.StopTimer()
	if len(samples) != b.N {
		b.Fatalf("%s latency samples=%d, want %d", name, len(samples), b.N)
	}
	slices.Sort(samples)
	b.ReportMetric(float64(len(samples)), name+"_samples")
	for _, percentile := range []int{50, 95, 99} {
		rank := (len(samples)*percentile + 99) / 100
		b.ReportMetric(float64(samples[rank-1].Nanoseconds()), fmt.Sprintf("%s_p%d_ns", name, percentile))
	}
}
