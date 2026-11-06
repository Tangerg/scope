// Package together wraps Together AI's OpenAI-compatible API.
// Together hosts hundreds of open-weight models (Llama, DeepSeek,
// Qwen, Mistral, etc.) with serverless and dedicated endpoints.
//
// Together-specific knobs reachable through the namespaced OpenAI request
// extension:
//
//   - "echo" / "min_p" / "repetition_penalty" are
//     accepted on top of the standard openai surface.
//   - The "safety_model" field enables Llama Guard prefilter / postfilter
//     by naming a guard model.
//
// Top-k sampling belongs to Core Options.TopK. The raw request extension
// rejects top_k even when the Core option is absent.
//
// See https://docs.together.ai/ for the full reference.
package together
