// Package assemblyai wraps AssemblyAI's speech-to-text API.
//
// [NewTranscriptionModel] orchestrates the upload → submit →
// poll → fetch flow against AssemblyAI's async /v2/transcript
// endpoints. [transcription.Options].Model selects the primary official model;
// use "universal-3-5-pro" for the current frontier model and add
// "universal-2" to the native speech_models option as a fallback.
//
// Provider extras (speaker diarization, auto chapters, sentiment
// analysis, entity detection, PII redaction, content safety,
// language detection) use official JSON names under [RequestExtensionKey].
//
// See https://www.assemblyai.com/docs for the full reference.
package assemblyai
