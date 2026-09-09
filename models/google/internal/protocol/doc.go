// Package protocol implements the Google Gen AI wire protocol reused by Gemini
// and Vertex AI provider endpoints inside the models module.
//
// Constructors:
//
//   - [NewChat] — native genai chat. Full Gemini surface:
//     thinking budget, response modalities, system instructions,
//     safety settings, structured output, tool calling, grounding
//     with Google Search;
//   - [NewEmbeddingModel] — gemini-embedding-2
//     with output_dimensionality truncation;
//   - [NewImageModel] — Gemini image generation through Interactions;
//   - [NewAudioTTSModel] — Gemini-TTS via generate_content with
//     audio response modality;
//   - [NewAudioTranscriptionModel] — audio-input → text via
//     generate_content (Gemini transcribes any audio attachment).
//
// Token estimation: [NewTextEstimator] wraps CountTokens for
// model-specific tokenizer-based counts.
//
// The Interactions request carries no Api-Revision header. Google documents
// that header as a way to pin a dated revision of the surface, and also
// documents breaking changes to it, so an unpinned caller can be moved by a
// change to the default revision. Every official example nonetheless sends
// only x-goog-api-key and Content-Type, which is what this posts: matching the
// documented access shape is the choice here, because a pinned date is itself a
// value that goes stale, and Scope cannot pick one on a caller's behalf. A
// caller who wants the pin supplies an [http.Client] that adds the header.
//
// Gemini's Context Caching API (cheaper repeated prompts) doesn't fit
// core/chat's request model and is not exposed.
//
// genai supports two backends: Generative Language (api key) and
// Vertex AI has its own facade package and construction config.
package protocol
