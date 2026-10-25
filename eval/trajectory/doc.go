// Package trajectory records and evaluates deterministic Agent execution facts.
// It keeps Agent vocabulary outside the subject-agnostic eval kernel and owns no
// experiment persistence, replay scheduler, or product workflow.
// Its strict JSON record preserves activation-local order and explicit semantic
// coverage. Take joins a root subtree before consuming its bounded recording.
// CheckCoverage checks history and semantic observations independently of token
// accounting or success; HistoryComplete alone only checks Process history.
// RootUsage and TreeUsage have distinct scopes. Unknown token accounting or
// elapsed time cannot satisfy an upper bound. Cross-restart history remains a
// fragment for historical comparisons; raw wall-clock order is not causal.
// Model calls retain interaction.ModelOutcome as their one outcome vocabulary.
// A missing settlement observation remains unobserved, separate from a provider
// outcome reported as unknown. NotExecuted proves zero provider token use;
// Failed without usage evidence leaves token accounting incomplete. A valid
// physical response remains recorded even when its durable settlement fails.
//
// The checked reference-task example composes Interaction, a structured Tool
// input wait, one isolated business write, and reconciliation of a lost response.
// Agent owns execution and wait transitions, TreeCommitter owns acknowledged
// heads, and the external target owns business receipts. Recording and grading
// only project these facts. No session, authorization, or user interface is added.
//
// Its fixed dataset grades execution, final-answer quality, business evidence,
// token cost, and observation coverage independently. It regrades retained JSON
// without another generation and compares paired cases under the same fixture
// and Metric identity. A changed token budget changes Decision identity, not
// measured cost. Rejection, repeated input, unresolved writes, incorrect answers,
// and over-budget runs remain in the dataset. Missing token accounting leaves
// the cost gate incomplete rather than claiming zero cost or observation loss.
// The deterministic model and scripted usage prove protocol composition,
// not live-model task quality. MemoryTreeCommitter proves only its in-process
// contract: real storage adapters must run agenttest.RunTreeCommitterConformance
// and separately prove database transactions, process crashes, and blocked I/O.
// An external business receipt and an Agent head are independent acknowledgments;
// this example does not claim atomic commit across them.
package trajectory
