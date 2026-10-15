// Package providerconformance checks the cross-provider consistency that no
// single provider module can check for itself: that every provider constructs
// the same way, exposes the same shape, and classifies failures with the same
// vocabulary. It is development tooling; product modules never depend on it.
//
// Provider modules run core/modeltest against themselves, which proves that one
// provider obeys the protocol. Agreement across the family needs every provider
// visible at once, which a provider module must not have because providers
// never import siblings.
//
// Model constructor checks discover providers from the source tree, and the web
// suites fail when a provider asserting a web contract is missing from them. A
// provider that fails a check is inconsistent with the family: fix the provider
// before relaxing the check.
//
// Chat evidence reuses Core modeltest and the native protocol corpus for four
// official bindings: OpenAI Chat Completions and Responses, Anthropic Messages,
// and Google GenerateContent. From this module directory, run:
//
//	SCOPE_TEST_CHAT_EVIDENCE_OUTPUT=/tmp/scope-chat-evidence.json go test -count=1 -timeout=13m -run '^TestChatEvidence$' -v .
//
// The report records the source revision, dirty state, Go version, UTC collection time, exact test
// names, and outcomes. offline_protocol proves adapter semantics against local
// SDK transports; offline_binding exercises the public constructors with text,
// tool-result replay, and JSON Schema. Neither proves live model behavior.
// Support is supported or unsupported only after its evidence test passes;
// skipped, missing, incomplete, and failed tests retain unknown support. A
// passing negative test means precisely the named operation is unsupported.
// It says nothing about other operations or other models.
//
// Live calls require an explicit comma-separated selection, for example:
//
//	SCOPE_TEST_CHAT_LIVE=openai-chat,openai-responses SCOPE_TEST_CHAT_EVIDENCE_OUTPUT=/tmp/scope-chat-live.json go test -count=1 -timeout=13m -run '^TestChatEvidence$' -v .
//
// Dedicated environment variables have no model defaults or credential fallbacks:
//
//   - openai-chat: SCOPE_TEST_OPENAI_KEY, SCOPE_TEST_OPENAI_CHAT_MODEL
//   - openai-responses: SCOPE_TEST_OPENAI_KEY, SCOPE_TEST_OPENAI_RESPONSES_MODEL
//   - anthropic-messages: SCOPE_TEST_ANTHROPIC_KEY, SCOPE_TEST_ANTHROPIC_MESSAGES_MODEL
//   - google-generate-content: SCOPE_TEST_GOOGLE_KEY, SCOPE_TEST_GOOGLE_CHAT_MODEL
//
// The evidence entry removes ambient provider SDK settings from its child processes.
// Selected bindings must have every required variable before any live request.
// Tests use the fixed official HTTPS origin, refuse redirects and alternate
// origins, cap response bodies at 2 MiB, and allow at most eight physical HTTP
// requests per binding, including SDK retries. Each generation requests at most
// 256 output tokens. Each HTTP request has a 30-second timeout and each scenario
// uses at most four requests; the evidence run has a 12-minute deadline.
// The only tool returns a local fixed test value. Output tokens are not a
// monetary budget; the selected model's provider pricing still applies.
//
// Unselected live cases are unverified. Selected configuration errors or failed
// assertions fail the evidence test while retaining the evidence report. A
// provider error is never treated as unsupported. Test output and credentials
// are excluded from the report. Run the evidence entry from a clean checkout when
// retaining evidence intended to identify one exact source revision.
package providerconformance
