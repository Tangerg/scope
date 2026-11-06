// Package coordination compiles bounded coordination policies into ordinary
// Programs. [Config.Policy] selects [InputGate], [Deadline], or [FirstSuccess];
// [Compile] is their sole construction path.
//
// InputGate declares one local Wait and returns its admitted answer directly,
// under AnswerSchema. Signal admission records own delivery identity; wrapping
// that identity in the output would make a maximum-size admitted answer exceed
// the output limit. Answer size and schema are fixed when the Wait opens. A new
// question requires a new gate identity; an ambiguous delivery must never be
// redirected to a replacement recipient.
//
// Deadline dispatches one absolute deadline with SameIdentity replay. FirstSuccess
// admits candidate children, selects an acceptable completed result, cancels the
// remaining candidates, and consumes every Exit before returning the winner.
// A cancellation receipt confirms intent; it does not establish resource release.
// All candidate starts must settle before selection, and ties within one input
// window follow candidate declaration order.
//
// These policies own business progress only. The Runtime owns scheduling,
// persistence, admission, cancellation, and drain. Completion and Exit messages
// must match outstanding identities; no child-wait or settlement handshake exists.
package coordination
