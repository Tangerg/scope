// Package speech defines the stable text-to-speech protocol and independent
// synchronous [Model] and optional [Streamer] provider capabilities.
//
// NewRequest captures text and Options carries explicit voice, format, speed,
// and JSON-safe provider overrides written through Options.Extensions.Set. Request has no
// arbitrary parameter bag. Streamer is separate from Model so consumers only
// require streaming from implementations that actually support it.
// [ReadChunks] preserves byte ownership and read-error ordering for adapters
// that stream raw audio; transport lifetime stays with the adapter.
// [Collect] bounds aggregate audio before appending each chunk and discards
// partial output on any stream failure. A provider's Stream remains incremental;
// the aggregate byte budget applies to its synchronous Call.
package speech
