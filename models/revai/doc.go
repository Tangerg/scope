// Package revai wraps Rev AI's speech-to-text API.
//
// [NewAudioTranscriptionModel] orchestrates Rev AI's async /v1/jobs
// flow (submit → poll → fetch). Rev AI's strength is enterprise-
// grade accuracy on English / multilingual content plus rich
// metadata (speaker channels, custom vocabularies, profanity
// filter, language ID).
//
// Set language through transcription.Options.Language and select machine or
// human transcription through transcription.Options.Model. Provider extras
// such as speaker_channels_count, custom_vocabulary_id, and remove_disfluencies
// belong in Options.Extensions under RequestExtensionKey.
//
// See https://docs.rev.ai/ for the full reference.
package revai
