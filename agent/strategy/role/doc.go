// Package role is a convenience facade that presents an interaction as a
// business-typed, composable Deployment: [New] wraps an interaction with input and
// output adapters, composing the canonical workflow and interaction atoms into one
// plain [agent.Deployment]. It adds no execution, working context, or persistence
// of its own, so the result stays composable like any other.
//
// role is the sanctioned composition tier: unlike the strategies, which never
// import one another, role may depend on the specific strategies it adapts.
package role
