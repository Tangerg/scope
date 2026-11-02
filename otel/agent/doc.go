// Package agent projects committed Agent logs into the official OpenTelemetry
// APIs without giving instrumentation control over execution or persistence.
//
// Observer.Consume reads Handle.Observe and returns the last projected LSN.
// The Host owns reader scheduling and cursor persistence. Recovery preserves
// the log position, so a new reader can continue without inventing another
// activation identity or replaying observations already exported. A resumed
// reader never fabricates the start or duration of an earlier Process or Effect.
//
// Invocation and Effect spans use acknowledged record timestamps. They describe
// logical durable work, including recovery delays. WrapDispatcher separately
// instruments physical I/O and propagates its span to downstream SDK calls.
// WrapStore preserves Append and Load exactly while measuring their duration
// and proposed batch size; a failed acknowledgment is never classified as rollback.
//
// Prompts, questions, outputs, credentials and failure diagnostics never become
// telemetry attributes. IDs occur only on spans, not metric dimensions. A
// canceled consumer ends unmatched spans as incomplete without claiming that
// the underlying Process or Effect failed. Close cancels and joins active
// consumers; the Host closes the runtime independently.
package agent
