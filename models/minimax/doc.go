// Package minimax exposes MiniMax chat adapters. [NewChatCompletions] targets its
// OpenAI-compatible endpoint; [NewMessages] targets its
// Anthropic-compatible endpoint.
// Structured reasoning keeps text in Part.Text and only native replay fields
// in ReasoningState. Stored states containing a duplicate text payload are rejected.
package minimax
