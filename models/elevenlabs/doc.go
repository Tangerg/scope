// Package elevenlabs wraps ElevenLabs' voice APIs.
//
// Two modalities are exposed:
//
//   - /v1/text-to-speech/{voice_id} via [NewSpeechModel] —
//     synthesizes speech from text. ElevenLabs is voice-first:
//     every call needs a voice id (the cloned or pro voice) which
//     is supplied through [speech.Options].Voice;
//   - /v1/speech-to-text via [NewTranscriptionModel] —
//     transcribes audio with speaker diarization, language id, and
//     timestamps; uses the Scribe v2 model family.
//
// ElevenLabs' voice cloning / library / projects surfaces aren't
// modeled here — they don't fit Core speech and transcription
// interfaces. Use the REST API directly for those.
//
// See https://elevenlabs.io/docs/api-reference for the full
// reference.
package elevenlabs
