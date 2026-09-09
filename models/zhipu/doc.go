// Package zhipu exposes Zhipu AI chat and embedding adapters. [NewChat]
// targets its OpenAI-compatible endpoint; [NewMessages] targets its
// Anthropic-compatible endpoint.
//
// GLM's OpenAI-compatible endpoint documents a narrower temperature range than
// OpenAI's: "temperature 参数的区间为 (0,1)，do_sample = False (temperature = 0)
// 在 OpenAI 调用中并不适用". The endpoint is not documented as clamping an
// out-of-range value, so nothing is refused here on its behalf — but Core's
// wider range is not all usable there, and 0 in particular does not mean what
// it means on OpenAI.
package zhipu
