// Package planning provides goal-directed state planning as an Agent execution
// strategy. Goal, Condition, WorldState, Action, and Plan belong exclusively to
// this package; the Agent kernel sees only opaque Execution state and Effects.
//
// Planning separates predicted Action semantics from external execution. A
// Planner is a pure, deterministic search over a Problem. A managed Planning
// Execution reads the real world through a Sensor and executes selected Actions
// outside its Step through a Deployment-bound dispatcher or a child Process,
// then senses again before accepting that the prediction became true. A Sensor
// supplies decision input; it has no role in execution telemetry.
package planning
