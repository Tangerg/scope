// Package gladia wraps Gladia's speech-to-text API.
//
// [NewTranscriptionModel] orchestrates Gladia's async pipeline
// (upload → /v2/transcription submit → poll → fetch). Gladia's
// supports the official Solaria-3 and Solaria-1 models. Solaria-1 provides
// broad multilingual coverage and code switching; Solaria-3 targets one
// configured European language. Speaker diarization and add-ons like
// summarization, translation, named-entity recognition, and audio
// intelligence — all reachable via extension-threaded
// TranscriptionRequest fields.
//
// See https://docs.gladia.io/ for the full reference.
package gladia
