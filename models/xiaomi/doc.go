// Package xiaomi exposes Xiaomi MiMo chat adapters. [NewChat] targets its
// OpenAI-compatible endpoint; [NewMessages] targets its
// Anthropic-compatible endpoint.
//
// On the OpenAI-compatible endpoint Core's MaxOutputTokens travels as
// max_completion_tokens, the only output-limit field MiMo documents there and
// the one it defines as bounding visible output and reasoning tokens together.
// The Anthropic-compatible endpoint keeps that protocol's own max_tokens.
//
// MiMo publishes what it discards, and [NewChat] refuses those settings rather
// than let them vanish. "When tool_choice passes non-auto values, backend
// defaults to removing the field, model response behavior remains equal to auto
// mode", so a request asking for a named or required tool would silently get
// free choice. In thinking mode the models "do not support custom temperature
// and top_p parameters. Even if passed, actual values forced to defaults 1.0
// and 0.95" -- and thinking is the documented default, so that is the ordinary
// case rather than an opt-in one. Disabling thinking through
// [ChatRequestOptions] restores both.
//
// Temperature is additionally bounded at the documented 1.5.
//
// See https://mimo.mi.com/docs for the reference.
package xiaomi
