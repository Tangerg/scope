// Package fireworks wraps Fireworks AI's OpenAI-compatible API.
// Fireworks hosts open-weight models on its FireAttention serving
// stack and ships latency-optimized custom variants of popular
// models.
//
// Fireworks-specific knobs reachable through the namespaced OpenAI request
// extension:
//
//   - "context_length_exceeded_behavior" controls truncation policy. Its
//     default matters to Core's MaxOutputTokens: where OpenAI "returns an
//     invalid request error" when the prompt plus the limit exceeds the
//     context window, Fireworks "automatically adjusts max_tokens to fit
//     within the context window", so the bound a caller set can come back
//     lowered. The shortened output still reports a length finish reason, and
//     "error" restores OpenAI's behavior.
//   - "prompt_cache_max_len" enables Fireworks' prompt-cache layer.
//   - The /chat/completions endpoint accepts "response_format" with
//     "type":"grammar" to constrain output via GBNF (alongside the
//     standard "json_schema").
//
// See https://docs.fireworks.ai/ for the full API reference.
package fireworks
