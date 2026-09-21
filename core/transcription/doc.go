// Package transcription defines the serializable audio-to-text protocol and
// its single-method [Model] capability.
//
// NewRequest accepts validated media audio. Options carries the shared language
// hint and provider extensions written through Options.Extensions.Set so they are JSON-safe.
// Request has no arbitrary parameter bag. Implementations and defaults live
// outside Core.
package transcription
