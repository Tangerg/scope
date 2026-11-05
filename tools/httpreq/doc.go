// Package httpreq exposes a single model-callable HTTP-request tool. It uses net/http and enforces host, method, redirect, timeout, and
// response-size policy at one client boundary.
//
// The model chooses every URL, so the policy assumes a hostile caller. Host
// patterns bind a port as well as a name. Names are judged before sending, but
// DNS answers only at dial time: the client's own transport refuses private,
// loopback, link-local and special-purpose addresses that are not explicitly
// allowed, including those a permitted name or redirect resolves to. It connects
// directly, because a proxy would choose the address instead. A host-supplied
// transport owns where its connections go.
//
// The host redirect policy can refuse a redirect but cannot extend the allowlist.
// Cross-origin redirects discard carried headers and refuse body replay.
// The host allowlist is mandatory; there is no "allow all" mode.
// A request the client refuses before sending is a definite core/tool.Failure:
// rejected for host or method policy, failed for invalid input. A redirect the
// policy refuses stays an ordinary error, because the first request was sent.
// An interrupted response remains an error. Received status, headers, and body
// bytes are preserved as core/tool.CallError evidence, not a complete tool result.
// Body and header values use content.Content; UTF-8 text is readable and binary
// data, partial characters, and HTTP obs-text remain lossless.
package httpreq
