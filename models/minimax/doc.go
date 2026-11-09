// Package minimax exposes MiniMax chat adapters. [NewChatCompletions] targets its
// OpenAI-compatible endpoint; [NewMessages] targets its
// Anthropic-compatible endpoint.
// Core Options.ReasoningEffort is admitted only for MiniMax-M3.1-Flash,
// whose native levels are low, medium, high, xhigh, and max. The resolved
// model owns this admission for both protocols; missing catalog effort metadata
// cannot disable controls supported by this adapter. M3.1-Flash cannot disable
// thinking, merge reasoning into answer content, or select a service tier.
// Structured reasoning keeps text in Part.Text and only native replay fields
// in ReasoningState. An explicit empty reasoning_details array retains an opaque
// reasoning Part so [] survives message storage and replay; absent or null arrays
// create no state. Replay state contains native arrays with zero or one detail
// and rejects object frames or duplicate Core text.
// Plain reasoning_content also survives storage and tool-result continuations.
// Its issuer and wire field remain attached to the Core text; foreign reasoning
// state is never reissued as MiniMax reasoning_content.
// Messages admits only automatic tool choice; unsupported required, named,
// and disabled choices fail before provider I/O. Chat Completions requests
// stream_options.include_usage through the shared protocol policy so terminal
// accounting survives Call and Stream; absent usage remains unknown.
package minimax
