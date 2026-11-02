// Package managed serves a Scope run as an A2A task. StartID derives its root
// identity; the committed log supplies status, artifacts and outstanding
// questions. Reading or reconnecting never acquires a writer epoch.
// Hosts supply authentication and resource authority; the protocol token and
// message/context IDs are not credentials. If the initial commit has no
// acknowledgment, retry the same message and context IDs with the same
// input to reconcile its Started record. Changing the input conflicts with an
// accepted identity; choosing a new message ID can start separate work.
//
// SendMessage accepts one plain text part or JSON carried losslessly as raw
// bytes or text. SDK Data values with numbers cannot prove their original
// precision and are rejected. Files and lossy multi-part conversions are
// rejected before starting work. String results are text artifacts; other
// results keep their exact JSON representation.
//
// An input-required status lists every pending question, its owner and schema.
// Reply to the task/context with the chosen scope.wait address (process and
// local key) in message metadata.
// Repeating a consumed answer reconciles through the committed SignalAdmitted record,
// even after the original writer exits. CancelTask requests termination and
// returns the observed status; only an acknowledged terminal head proves it
// stopped. A different host can read or subscribe but cannot take over to act.
//
// This handler does not provide task enumeration, message history, push
// notification configuration or an extended card. Hosts own HTTP authentication,
// transport deadlines, runtime shutdown and durable-store retention.
package managed
