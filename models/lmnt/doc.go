// Package lmnt wraps LMNT's TTS API.
//
// LMNT has shut down. Its documentation site now carries only an
// announcement — "LMNT has shut down" and "Our speech generation journey has
// come to an end" — with no sunset date and no migration path, so
// [NewAudioTTSModel] has no live service to reach and every call fails at the
// transport. Nothing in this package is wrong; its provider is gone. Whether
// the module stays or goes is a decision about the published module, not about
// this code, so it is recorded here rather than acted on.
//
// [NewAudioTTSModel] targets LMNT's /v1/ai/speech endpoint. LMNT was
// optimized for ultra-low-latency synthesis (~300ms first-byte) on
// the Blizzard and Aurora voice families, with conversational
// pacing well suited to voice agents.
//
// Provider-specific knobs (speed, format, sample_rate, conversational
// mode, language, seed) ride through extension-threaded SpeechRequest
// fields. The portable [speech.Options.Speed] is refused rather than mapped,
// because the bytes endpoint this package posts to did not accept it.
//
// See https://docs.lmnt.com/ for what remains of the reference.
package lmnt
