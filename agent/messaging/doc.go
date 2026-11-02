// Package messaging delivers intermediate input through dispatcher Effects.
// [Message] freezes one recipient, optional WaitKey, and payload. [Dispatcher]
// derives a SignalID from the sending EffectID and uses one host-bound
// [DeliveryPort] to admit it. The recipient's Log owns deduplication and admission.
//
// The port must authorize sender and recipient, validate the receiver's contract,
// and preserve the concrete destination across replay. A duplicate admission is
// successful delivery. Every port error remains uncertain because it cannot
// disprove an earlier admission, including when the recipient is now terminal.
// A port can reconcile exact SignalAdmitted records before reporting an error.
// A missing record in a stale Log establishes nothing.
//
// SameIdentity replay retains the exact Signal. No cross-tree transaction or
// runtime-attested origin is implied. Strategies controlling direct children use
// run.NewChildSignalEffect instead; this capability serves host-authorized
// delivery across other ownership boundaries.
package messaging
