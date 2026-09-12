// Package coordination provides bounded coordination through ordinary Agent
// Definitions. InputGate turns one addressed input into a result carrying the
// original Signal identity. Deadline participates through the cancellable Timer
// Dispatcher. FirstSuccess owns a competition among exact child requests.
//
// These Definitions use the public Step, Effect, Signal, and child-wait protocol.
// They have no scheduler, mailbox, journal, or lifecycle authority of their own.
// A Deployment supplies exact implementation and configuration digests; the
// configuration digest must cover schemas, bounds, and decision policies.
//
// A competition selects among observed terminal results, in request order within
// one satisfaction Signal, after all candidate starts have settled. Slow
// admission delays selection even when an earlier child has already completed.
// Completing the competition cancels its remaining descendants. Use the drained
// child-wait boundary or Process.Join before reusing their exclusive resources.
//
// Gates and competitions consume finite child, Effect, and Signal allocations.
// Gate replacement changes the recipient address. A delivery must remain bound
// to its original Process and WaitID until its admission and disposition are
// known; retrying against a replacement can consume the same input twice.
package coordination
