// Package moonshot exposes Moonshot AI chat adapters. [NewChat] targets its
// OpenAI-compatible endpoint; [NewMessages] targets its
// Anthropic-compatible endpoint.
//
// On the OpenAI-compatible endpoint Core's MaxOutputTokens travels as
// max_completion_tokens, and Temperature is bounded at 1: Moonshot's migration
// guide states that "Kimi API 的 temperature 参数的取值范围是 [0, 1]，而
// OpenAI 的 temperature 参数的取值范围是 [0, 2]", so a larger value is refused
// rather than sent out of range.
//
// Two documented differences stay the caller's, because both depend on the
// model rather than the endpoint. tool_choice "required" is rejected by
// kimi-k2.7-code and kimi-k2.6 while kimi-k3 accepts it, and kimi-k2.6 fixes
// temperature at 1.0 in thinking mode and 0.6 outside it, erroring on anything
// else. Neither can be enforced here without a model table this package would
// then have to keep current.
package moonshot
