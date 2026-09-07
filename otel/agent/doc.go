// Package agent adapts Agent Framework events to the official OpenTelemetry
// tracing and metrics APIs. The Agent Kernel does not import OpenTelemetry.
//
// Each Process activation is an in-process invoke_agent span named after its
// Deployment, with matching gen_ai.invoke_agent.duration. A restored activation
// starts a new observation interval; the process activation attribute separates
// it from an initial invocation. Process IDs remain runtime attributes, not
// stable gen_ai.agent.id values. Model selection stays at the model boundary.
// Durable Process, Step, and Effect spans are keyed by the active incarnation,
// so overlapping old and restored instances never share span ownership.
//
// Register an Observer as an Engine EventListener and wrap each Deployment's
// Dispatcher with Observer.WrapDispatcher to make downstream model and tool
// spans children of their Effect span. Close the Engine before the Observer.
// Wrap the selected TreeDurability with Observer.WrapTreeDurability to observe
// protocol acknowledgment duration, proposed snapshot bytes, and conflict or
// unresolved outcomes. This integration does not select storage, read heads,
// infer rollback, or measure the age of stored state.
//
// Failed invocations use the declared Failure code as error.type. Other error
// terminations use agent.<termination cause>; Step and Effect failures use
// agent.step.failed and agent.effect.<settlement status>. Kernel fact messages
// contain these stable classifications, never application failure diagnostics.
// RuntimeStopped ends the affected instance's spans and activation duration
// without recording a logical Process exit or terminal usage measurements.
package agent
