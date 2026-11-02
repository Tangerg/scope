// Package agent adapts Agent Framework events to the official OpenTelemetry
// tracing and metrics APIs. The Agent Kernel does not import OpenTelemetry.
//
// Each Process activation is an in-process invoke_agent span named after its
// program, with matching gen_ai.invoke_agent.duration. A restored activation
// starts a new observation interval; the process activation attribute separates
// it from an initial invocation. Process IDs remain runtime attributes, not
// stable gen_ai.agent.id values. Model selection stays at the model boundary.
// Durable Process, Step, and Effect spans are keyed by the active incarnation,
// so overlapping old and restored instances never share span ownership.
// A physical AttemptID pairs an Effect start and finish, so a dropped finish
// cannot make its replay inherit the previous span. Process termination or
// Observer.Close ends an unmatched start as incomplete.
// Effect spans and dropped-Delta events carry the Runtime's physical AttemptID.
// Replays keep the logical EffectID and receive a fresh AttemptID. Unknown
// resolution is a Process event with the definite settlement, not another
// invocation or an extension of the original duration. These identities never
// become metric dimensions.
//
// Register an Observer as a run.EventListener and wrap each program's
// Dispatcher with Observer.WrapDispatcher to make downstream model and tool
// spans children of the actual dispatch span. Close the Runtime before the Observer.
// Wrap the selected run.Store with Observer.WrapTreeCommitter to observe
// protocol acknowledgment duration, proposed snapshot bytes, and conflict or
// unresolved outcomes. This integration does not select storage, read heads,
// infer rollback, or measure the age of stored state.
// Its duration covers the decorated committer call; tree capture, encoding,
// admission, and complete recovery latency belong to the caller's measurements.
// BenchmarkTreeCommitterObservation isolates SDK observation overhead using
// prebuilt checkpoints, a no-I/O committer, and no exporter I/O. Compare it with
// the Agent module's commit/recovery benchmarks before sizing a Host deployment.
//
// Failed invocations use the declared Failure code as error.type. Other error
// terminations use agent.<termination cause>; Step and Effect failures use
// agent.step.failed and agent.effect.<settlement status>. Kernel fact messages
// contain these stable classifications, never application failure diagnostics.
// Unknown attempts caused by Dispatcher errors retain the fact's Failure kind
// and code; a directly returned Unknown does not invent a failure cause.
// RuntimeStopped ends the affected instance's spans and activation duration
// without recording a logical Process exit or terminal usage measurements.
package agent
