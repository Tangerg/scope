// Package xiaomi exposes Xiaomi MiMo chat adapters. [NewChat] targets its
// OpenAI-compatible endpoint; [NewMessages] targets its
// Anthropic-compatible endpoint.
//
// On the OpenAI-compatible endpoint Core's MaxOutputTokens travels as
// max_completion_tokens, the only output-limit field MiMo documents there and
// the one it defines as bounding visible output and reasoning tokens together.
// The Anthropic-compatible endpoint keeps that protocol's own max_tokens.
//
// See https://mimo.mi.com/docs for the reference.
package xiaomi
