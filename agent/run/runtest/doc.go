// Package runtest exercises public execution and append-log contracts with
// deterministic fixtures. Store conformance injects failures around durable
// acknowledgment because a transport failure cannot prove whether a write
// committed. Definition conformance compares exact transitions and restored
// state without reaching into runtime ownership.
package runtest
