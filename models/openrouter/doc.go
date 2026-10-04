// Package openrouter exposes OpenRouter chat adapters. [NewChatCompletions] targets its
// OpenAI-compatible endpoint; [NewMessages] targets its
// Anthropic-compatible endpoint.
//
// Set chat.Options.ReasoningEffort for the chat reasoning effort. The raw
// OpenAI request extension cannot also set reasoning.effort; other native
// reasoning settings remain available through that extension.
// Structured reasoning keeps each native detail as a separate Core Part:
// Part.Text owns visible text, while ReasoningState retains native replay fields.
// An explicit empty reasoning_details array retains an opaque reasoning Part so
// [] survives message storage and replay; absent or null arrays create no state.
// Replay state contains native arrays with zero or one detail and rejects object
// frames or duplicate Core text. Preserve signed reasoning content and native
// fields when replaying provider history.
package openrouter
