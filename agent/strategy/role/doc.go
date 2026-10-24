// Package role is a convenience facade that presents an interaction as a
// business-typed, composable Deployment. The repeated assembly — adapt a business
// input into an [interaction.Input], run the interaction, project its
// [interaction.Output] into a business result — otherwise reappears at every call
// site. [New] composes the canonical workflow and interaction atoms into one plain
// [agent.Deployment]; it introduces no new execution, working context, or
// persistence, and the produced Deployment stays composable like any other.
//
// role is the sanctioned composition tier: unlike the strategies, which never
// import one another, role may depend on the specific strategies it adapts.
package role
