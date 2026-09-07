// Package agent adapts Agent Framework events to the official OpenTelemetry
// tracing and metrics APIs. The Agent Kernel does not import OpenTelemetry.
//
// Each Process activation is an in-process invoke_agent span named after its
// Deployment, with matching gen_ai.invoke_agent.duration. A restored activation
// starts a new observation interval; the process activation attribute separates
// it from an initial invocation. Process IDs remain runtime attributes, not
// stable gen_ai.agent.id values. Model selection stays at the model boundary.
//
// Register an Observer as an Engine EventListener and wrap each Deployment's
// Dispatcher with Observer.WrapDispatcher to make downstream model and tool
// spans children of their Effect span. Close the Engine before the Observer.
//
// Failed invocations use the declared Failure code as error.type. Other error
// terminations use agent.<termination cause>; Step and Effect failures use
// agent.step.failed and agent.effect.<settlement status>. Kernel fact messages
// contain these stable classifications, never application failure diagnostics.
package agent
