// Package messaging delivers intermediate input through ordinary Dispatcher
// Effects. A Message freezes one concrete recipient, optional WaitID, and payload.
// Dispatcher derives its SignalID from the sending EffectID and submits exactly
// one Signal through a Host-bound DeliveryPort. Mailboxes retain admission,
// deduplication, accounting, and committed consumption ownership.
//
// The port must authorize the sender and concrete recipient and validate the
// receiver's payload contract. A logical address must be resolved before the
// Effect is prepared; replay must never select a replacement gate or episode.
// The sender and receiver have separate acknowledgment boundaries. No atomic
// cross-tree transaction or runtime-attested origin is implied by this adapter.
//
// A duplicate single-Signal admission is successful delivery. Every port error
// remains unknown, including a terminal recipient after an earlier ambiguous
// attempt. A port can reconcile against the original recipient's authoritative
// ProcessSnapshot.SignalReceipts. A matching receipt proves admission even
// after consumption; a missing receipt in an old snapshot proves nothing.
// Pending Effects may replay under the same identity. Settled Unknown results
// require explicit adjudication through Process.ResolveUnknownEffect.
package messaging
