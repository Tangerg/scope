// Package agent instruments Agent runtimes with the official OpenTelemetry APIs
// without giving instrumentation control over execution or persistence.
//
// Two values divide the work. Middleware decorates the Runtime's physical I/O
// from the outside, as every otel module decorates its protocol boundary:
// Middleware.Store wraps the run.Store, and Middleware.Dispatch is a
// run.DispatchMiddleware for Config.DispatchMiddlewares, so the Effects of
// every Program, built-in strategies included, are spanned. Observer projects
// the committed log into logical spans and metrics.
//
// Observer.Consume reads Handle.Observe and returns the last projected LSN.
// The Host owns reader scheduling and cursor persistence. Recovery preserves
// the log position, so a new reader can continue without inventing another
// activation identity or replaying observations already exported. A resumed
// reader never fabricates the start or duration of an earlier Process or Effect.
//
// Invocation and Effect spans use acknowledged record timestamps. They describe
// logical durable work, including recovery delays. Middleware.Dispatch
// separately spans each physical dispatch, including one repeated under the
// same EffectID, and propagates its span to downstream SDK calls.
// Middleware.Store preserves Append, Load and Read exactly while measuring
// their duration and proposed batch size; a failed acknowledgment is never
// classified as rollback. An unconstructed Middleware decorates into values
// whose every call fails with ErrInvalidConfig.
//
// Prompts, questions, outputs, credentials and failure diagnostics never become
// telemetry attributes. IDs occur only on spans, not metric dimensions. A
// canceled consumer ends unmatched spans as incomplete without claiming that
// the underlying Process or Effect failed. Close cancels and joins active
// consumers; the Host closes the runtime independently.
package agent
