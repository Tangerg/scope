// Package speech defines the stable text-to-speech protocol and independent
// synchronous [Model] and optional [Streamer] provider capabilities.
//
// NewRequest captures text and Options carries explicit voice, format, speed,
// and JSON-safe provider overrides written through Options.Extensions.Set. Request has no
// arbitrary parameter bag. Streamer is separate from Model so consumers only
// require streaming from implementations that actually support it.
package speech
