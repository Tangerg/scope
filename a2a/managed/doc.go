// Package managed serves a Scope run as an A2A task. The scoped start receipt
// identifies the task and its acknowledged tree supplies status, artifacts
// and outstanding questions. Reading or reconnecting never acquires a writer.
// Hosts supply authentication and resource authority; the protocol token and
// message/context IDs are not credentials.
//
// SendMessage accepts one plain text part or JSON carried losslessly as raw
// bytes or text. SDK Data values with numbers cannot prove their original
// precision and are rejected. Files and lossy multi-part conversions are
// rejected before starting work. String results are text artifacts; other
// results keep their exact JSON representation.
//
// An input-required status lists every pending question, its owner and schema.
// Reply to the task/context with the chosen scope.wait_id in message metadata.
// Repeating a consumed answer reconciles through the durable signal receipt,
// even after the original writer exits. CancelTask requests termination and
// returns the observed status; only an acknowledged terminal head proves it
// stopped. A different host can read or subscribe but cannot take over to act.
//
// This handler does not provide task enumeration, message history, push
// notification configuration or an extended card. Hosts own HTTP authentication,
// transport deadlines, runtime shutdown and durable-store retention.
package managed
