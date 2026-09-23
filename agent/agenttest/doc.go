// Package agenttest provides deterministic consumer-side fixtures and reusable
// conformance suites for the Agent Framework's public execution boundaries. It
// exercises public Definition, Execution, DeploymentResolver, and TreeCommitter
// contracts without simulating private Engine state or Process lifecycle
// ownership. Each suite covers the behavior its contract states in prose and no
// compiler checks; it never asserts one implementation arrangement.
package agenttest
