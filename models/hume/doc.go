// Package hume wraps Hume AI's TTS API.
//
// [NewSpeechModel] targets Hume's /v0/tts endpoint backed by the
// Octave voice model — Hume's pitch is emotion-aware synthesis
// driven by acting / description prompts in addition to plain text.
//
// Provider-specific description, voice.provider, trailing_silence, format
// details, timestamps, and instant_mode use SpeechRequestExtensionKey. One
// utterance may carry these controls; Core owns its text, voice ID, speed,
// and output format. [speech.Options].Model selects the
// official Octave protocol version ("1" or "2"). [NewStreamingSpeechModel]
// exposes streaming separately through Hume's
// newline-delimited /v0/tts/stream/json endpoint. Timestamp output belongs
// to unary synthesis; instant_mode belongs to streaming synthesis.
//
// Hume's broader expression-measurement APIs (face / voice / language
// emotion analysis) aren't exposed — they don't fit Core
// speech and transcription interfaces.
//
// See https://dev.hume.ai/docs for the full reference.
package hume
