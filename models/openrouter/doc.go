// Package openrouter exposes OpenRouter chat adapters. [NewChat] targets its
// OpenAI-compatible endpoint; [NewMessages] targets its
// Anthropic-compatible endpoint.
//
// Set chat.Options.ReasoningEffort for the chat reasoning effort. The raw
// OpenAI request extension cannot also set reasoning.effort; other native
// reasoning settings remain available through that extension.
package openrouter
