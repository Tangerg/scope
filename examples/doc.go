// Package examples contains runnable demonstrations, with no supported API.
// Examples may import Scope modules; Scope modules must not import examples.
//
// Command agent/reconciliation restores an Interaction with an uncertain remote
// write and investigates its receipt through the public settlement APIs. It
// includes confirmed success, confirmed rejection, missing receipts, and query
// failure. With GOWORK=off, it consumes the published versions in this module's
// go.mod, without local replacements or a separate consumer manifest.
package examples
