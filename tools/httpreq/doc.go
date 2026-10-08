// Package httpreq exposes a single model-callable HTTP-request tool. It wraps
// go-resty as the transport and enforces host, method, redirect, timeout, and
// response-size policy at one client boundary.
//
// The host allowlist is mandatory; there is no "allow all" mode.
// A request the client refuses before sending is a definite core/tool.Failure:
// rejected for host or method policy, failed for invalid input. A redirect the
// policy refuses stays an ordinary error, because the first request was sent.
// An interrupted response remains an error. Received status, headers, and body
// bytes are preserved as core/tool.CallError evidence, not a complete tool result.
// Body and header values use content.Content; UTF-8 text is readable and binary
// data, partial characters, and HTTP obs-text remain lossless.
package httpreq
