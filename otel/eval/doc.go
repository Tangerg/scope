// Package eval adapts Scope evaluation calls to the official OpenTelemetry
// tracing and metrics APIs. The eval module does not import OpenTelemetry; this
// module decorates it from outside so a core-only consumer never inherits the
// telemetry dependency.
//
// Wrap an [github.com/Tangerg/scope/eval.Assessment] with Middleware.Wrap to
// record an eval.evaluate span and an eval.operation.duration measurement per
// evaluation. The subject value is never recorded; only the metric namespace,
// name, verdict, decision policy, and score become attributes. A panicking
// evaluator ends the observation and re-panics the original value unchanged.
package eval
