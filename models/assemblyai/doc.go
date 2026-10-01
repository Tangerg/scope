// Package assemblyai wraps AssemblyAI's speech-to-text API.
//
// [NewTranscriptionModel] orchestrates the upload → submit →
// poll → fetch flow against AssemblyAI's async /v2/transcript
// endpoints. [transcription.Options].Model selects the primary official model;
// use "universal-3-5-pro" for the current frontier model and add
// "universal-2" to [TranscriptRequest.SpeechModels] as a fallback.
//
// Provider extras (speaker diarization, auto chapters, sentiment
// analysis, entity detection, PII redaction, content safety,
// language detection) ride through extension-threaded TranscriptRequest
// fields.
//
// See https://www.assemblyai.com/docs for the full reference.
package assemblyai
