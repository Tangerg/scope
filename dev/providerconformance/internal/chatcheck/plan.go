// Package chatcheck owns the bounded provider chat test plan and projects Go
// test events into evidence. It is not a runtime capability catalog.
package chatcheck

const (
	LiveSelectionEnv = "SCOPE_TEST_CHAT_LIVE"
	OutputEnv        = "SCOPE_TEST_CHAT_EVIDENCE_OUTPUT"
)

// Entry selects an official binding and the existing protocol tests that prove
// its offline semantics. Offline evidence applies to the protocol, not a live model.
type Entry struct {
	ID        string
	KeyEnv    string
	ModelEnv  string
	BaseURL   string
	Directory string
	Tests     []Case
}

type Case struct {
	Test      string `json:"test"`
	Semantics string `json:"semantics"`
	Support   string `json:"support"`
}

func Plan() []Entry {
	return []Entry{
		{
			ID: "openai-chat", KeyEnv: "SCOPE_TEST_OPENAI_KEY", ModelEnv: "SCOPE_TEST_OPENAI_CHAT_MODEL",
			BaseURL: "https://api.openai.com/v1", Directory: "models/protocol/openai",
			Tests: []Case{
				{Test: "TestCompatibleChat_CoreConformance", Semantics: "ordered_parts_tools_citations_usage", Support: "supported"},
				{Test: "TestChat_BehaviorConformance", Semantics: "cancellation_early_stop_first_error", Support: "supported"},
			},
		},
		{
			ID: "openai-responses", KeyEnv: "SCOPE_TEST_OPENAI_KEY", ModelEnv: "SCOPE_TEST_OPENAI_RESPONSES_MODEL",
			BaseURL: "https://api.openai.com/v1", Directory: "models/protocol/openai",
			Tests: []Case{
				{Test: "TestResponses_CoreConformance", Semantics: "ordered_parts_tools_citations_usage", Support: "supported"},
				{Test: "TestResponses_BehaviorConformance", Semantics: "cancellation_early_stop_first_error", Support: "supported"},
				{Test: "TestResponsesTerminalStatesMatchAcrossCallAndStream", Semantics: "terminal_states", Support: "supported"},
				{Test: "TestResponsesChatRejectsUnsupportedOptions", Semantics: "top_k", Support: "unsupported"},
			},
		},
		{
			ID: "anthropic-messages", KeyEnv: "SCOPE_TEST_ANTHROPIC_KEY", ModelEnv: "SCOPE_TEST_ANTHROPIC_MESSAGES_MODEL",
			BaseURL: "https://api.anthropic.com", Directory: "models/protocol/anthropic",
			Tests: []Case{
				{Test: "TestChat_CoreConformance", Semantics: "ordered_parts_tools_citations_usage", Support: "supported"},
				{Test: "TestChat_BehaviorConformance", Semantics: "cancellation_early_stop_first_error", Support: "supported"},
				{Test: "TestMessagesRequireCompleteTerminalSequence", Semantics: "terminal_states", Support: "supported"},
			},
		},
		{
			ID: "google-generate-content", KeyEnv: "SCOPE_TEST_GOOGLE_KEY", ModelEnv: "SCOPE_TEST_GOOGLE_CHAT_MODEL",
			BaseURL: "https://generativelanguage.googleapis.com", Directory: "models/google/internal/protocol",
			Tests: []Case{
				{Test: "TestChat_CoreConformance", Semantics: "ordered_parts_tools_reasoning_usage", Support: "supported"},
				{Test: "TestChat_BehaviorConformance", Semantics: "cancellation_early_stop_first_error", Support: "supported"},
				{Test: "TestProtocolToolChoiceUsesOneCoreSurface", Semantics: "single_tool_parallelism", Support: "unsupported"},
			},
		},
	}
}

func LiveCases(entry string) []Case {
	var cases []Case
	for _, scenario := range []string{"text", "tool_round_trip", "json_schema"} {
		cases = append(cases, Case{Test: "TestChatLive/" + entry + "/" + scenario, Semantics: scenario, Support: "supported"})
	}
	return cases
}
