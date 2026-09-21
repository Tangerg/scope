// Package hume wraps Hume AI's TTS API.
//
// [NewAudioTTSModel] targets Hume's /v0/tts endpoint backed by the
// Octave voice model — Hume's pitch is emotion-aware synthesis
// driven by acting / description prompts in addition to plain text.
//
// Provider-specific knobs (description, voice (named or cloned),
// trailing_silence, format, timestamps, and instant_mode) ride through
// extension-threaded [TTSRequest] fields. [speech.Options].Model selects the
// official Octave protocol version ("1" or "2"). [NewStreamingAudioTTSModel]
// exposes streaming separately through Hume's
// newline-delimited /v0/tts/stream/json endpoint. Timestamp output belongs
// to unary synthesis; instant_mode belongs to streaming synthesis.
//
// Hume's broader expression-measurement APIs (face / voice / language
// emotion analysis) aren't exposed — they don't fit core/model's
// tts/transcription interfaces.
//
// See https://dev.hume.ai/docs for the full reference.
package hume
