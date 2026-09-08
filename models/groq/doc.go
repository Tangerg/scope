// Package groq wraps Groq's OpenAI-compatible API. Groq runs open-
// weight models (Llama, Gemma, DeepSeek, Kimi) on its in-house LPUs
// at extremely high throughput.
//
// Groq-specific knobs reachable through the namespaced OpenAI request extension:
//
//   - service_tier ("on_demand" / "flex" / "auto") trades cost for
//     latency. See https://console.groq.com/docs/flex-processing.
//   - reasoning_format ("parsed" / "raw" / "hidden") controls how
//     reasoning-model output is surfaced.
//
// Output token limits go out as max_completion_tokens. Groq's API reference
// marks max_tokens "Deprecated in favor of max_completion_tokens" and its own
// text-generation guide passes the replacement, which is documented to bound
// reasoning tokens together with visible output — the field that caps what a
// reasoning model actually generates.
//
// See https://console.groq.com/docs/ for the full API reference.
package groq
