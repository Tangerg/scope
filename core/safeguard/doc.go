// Package safeguard provides fail-closed input and output screening as Core
// chat middleware.
//
// Matcher is the extension boundary for substring lists, classifiers, or
// remote moderation services. Middleware has no global registry or default
// policy: callers construct and compose an explicit instance.
package safeguard
