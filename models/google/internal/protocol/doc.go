// Package protocol implements the Google Gen AI wire protocol reused by Gemini
// and Vertex AI provider endpoints inside the models module.
//
// Constructors:
//
//   - [NewChat] — native genai chat:
//     thinking budget, response modalities, system instructions,
//     safety settings, structured output, tool calling, grounding
//     with Google Search. Code execution is rejected because its output and
//     replay state cannot be represented losslessly;
//   - [NewEmbeddingModel] — text embeddings with outputDimensionality
//     truncation; the selected model determines its supported dimensions;
//   - [NewStreamingSpeechModel] — Gemini 3.1 TTS; Call consumes Stream;
//   - [NewSpeechModel] — unary-only Gemini 2.5 TTS via generate_content with
//     audio response modality;
//   - [NewTranscriptionModel] — audio-input → text via
//     generate_content (Gemini transcribes any audio attachment).
//
// Token estimation: [NewTextCounter] wraps CountTokens for
// model-specific tokenizer-based counts.
//
// Gemini's Context Caching API (cheaper repeated prompts) doesn't fit
// core/chat's request model and is not exposed.
//
// genai supports two backends: Generative Language (api key) and
// Vertex AI has its own facade package and construction config.
package protocol
