// Package planning separates predicted action semantics from observed reality.
// Goal, Condition, WorldState, Action, and Plan belong to this strategy; the run
// kernel sees only opaque execution state and declared Effects.
//
// [Compile] freezes the planning domain, action bindings, and limits using the
// Runtime's Compiler. Revision identifies the opaque Sensor, Planner, and action
// implementations. The definition retains only action declarations; its
// dispatcher owns the Sensor and ActionExecutors and shares the same immutable
// action index, so execution cannot diverge from the declared authority.
// A Planner performs deterministic search over a Problem.
// A Sensor reads actual world state through a dispatcher Effect. Actions execute
// through dispatcher Effects or exact child Programs, then the strategy senses
// again before accepting that their predicted effects became true.
//
// Attempt facts determine eligibility. Successful actions remain current until
// sensing confirms their effects; failed or unconfirmed actions are excluded
// from later planning. Restore derives the same decisions from these facts.
// A child action is observed only through its drained Exit. Its admission or
// execution failure records a failed attempt and cannot establish world state.
//
// Unknown external outcomes require explicit host resolution before planning
// advances. Cancellation and subtree drain remain Runtime-owned. Unaddressed
// Signals are rejected, and messages must match the outstanding action, sensing
// Effect, or child invocation.
package planning
