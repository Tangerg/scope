package agent_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	agentotel "github.com/Tangerg/scope/otel/agent"
)

// The retained checkpoint is built before timing. The adapter acknowledges
// without storage so this isolates observation overhead from tree encoding and I/O.
func BenchmarkTreeCommitterObservation(b *testing.B) {
	for _, size := range []int{1 << 10, 8 << 20, 30 << 20} {
		b.Run(fmt.Sprintf("state_%d", size), func(b *testing.B) {
			checkpoint := observedCheckpoint(b, strings.Repeat("x", size))
			snapshotBytes := len(checkpoint.TreeSnapshot().JSON())
			for _, enabled := range []bool{false, true} {
				b.Run(fmt.Sprintf("observed_%t", enabled), func(b *testing.B) {
					next := &failingObservedDurability{}
					invoke := next.CommitCheckpoint
					if enabled {
						tracer := sdktrace.NewTracerProvider()
						meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader()))
						b.Cleanup(func() {
							if err := tracer.Shutdown(context.Background()); err != nil {
								b.Error(err)
							}
							if err := meter.Shutdown(context.Background()); err != nil {
								b.Error(err)
							}
						})
						observer, err := agentotel.NewObserver(agentotel.ObserverConfig{TracerProvider: tracer, MeterProvider: meter})
						if err != nil {
							b.Fatal(err)
						}
						b.Cleanup(observer.Close)
						observed, err := observer.WrapTreeCommitter(next)
						if err != nil {
							b.Fatal(err)
						}
						invoke = observed.CommitCheckpoint
					}
					b.ReportAllocs()
					for b.Loop() {
						if err := invoke(b.Context(), checkpoint); err != nil {
							b.Fatal(err)
						}
					}
					b.ReportMetric(float64(snapshotBytes), "snapshot_bytes")
				})
			}
		})
	}
}
