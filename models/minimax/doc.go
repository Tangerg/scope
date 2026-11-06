// Package minimax exposes MiniMax chat adapters. [NewChatCompletions] targets its
// OpenAI-compatible endpoint; [NewMessages] targets its
// Anthropic-compatible endpoint.
// Structured reasoning keeps text in Part.Text and only native replay fields
// in ReasoningState. An explicit empty reasoning_details array retains an opaque
// reasoning Part so [] survives message storage and replay; absent or null arrays
// create no state. Replay state contains native arrays with zero or one detail
// and rejects object frames or duplicate Core text.
// Plain reasoning_content also survives storage and tool-result continuations.
// Its issuer and wire field remain attached to the Core text; foreign reasoning
// state is never reissued as MiniMax reasoning_content.
package minimax
