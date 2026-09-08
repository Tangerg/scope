// Package bedrock wraps AWS Bedrock Runtime.
//
// Bedrock is a model-aggregation gateway — a single endpoint that
// fronts foundation models from Anthropic, Meta, Mistral, Amazon
// Titan / Nova, Cohere, AI21, Stability and others. [NewChat]
// uses the unified Converse / ConverseStream API which speaks a
// provider-agnostic message shape; [NewEmbeddingModel] targets the native
// InvokeModel contracts for Titan Text Embeddings V1/V2 and Cohere Embed
// V3/V4. Provider-only embedding controls use [EmbeddingRequestOptions] under
// [EmbeddingRequestExtensionKey].
//
// Model selection is via the upstream model id (e.g.
// "anthropic.claude-3-5-sonnet-20241022-v2:0",
// "amazon.titan-embed-text-v2:0", "meta.llama3-1-70b-instruct-v1:0",
// "us.anthropic.claude-sonnet-4-20250514-v1:0"). Bedrock supports
// regional and cross-region inference-profile IDs.
//
// AWS auth is handled by the standard aws-sdk-go-v2 chain (env vars,
// shared config, IRSA, instance role); no custom APIKey is required.
//
// Stop reasons. Converse reports two truncations — max_tokens for the budget
// the caller set and model_context_window_exceeded for the model's own window
// filling first — and both report as a truncation. Guardrail and content
// filtering report as a policy outcome; malformed model output and malformed
// tool use have no portable match and keep their native value in the output
// metadata.
//
// Stream termination. A messageStop event is the only claim that the message is
// whole. An event stream can end cleanly mid-message without the SDK reporting
// an error, so a stream that stops without one fails with
// [chat.ErrInvalidResponse] rather than passing off a partial answer.
//
// Token usage. With prompt caching, Converse reports inputTokens as the
// non-cached part only and the cache counts beside it, so the store adds the
// three into the total Core reports and keeps the cache counts as breakdowns of
// it. Copying inputTokens through would understate the input by the whole
// cached prefix and put a breakdown above its own total.
//
// Reasoning. Converse has no reasoning field: reasoning rides in
// additionalModelRequestFields, and its shape belongs to the model generation
// rather than to Converse — Claude 3.7 takes reasoning_config with a
// budget_tokens count, while newer models reject that form and take thinking
// with an output_config effort. A portable reasoning effort is therefore
// refused rather than translated into an invented token budget or a guessed
// generation; state the model's own parameters through the request
// extension's AdditionalModelRequestFields.
//
// Stream shape. ConverseStream sends its metadata event — the one carrying
// usage — after messageStop, so the finish reason is held and stamped onto
// whichever delta turns out to be last. Reporting the end at messageStop put
// the usage delta after a finished stream, and a [chat.ResponseAccumulator]
// rejects that: a consumer that already saw the answer end cannot fold
// anything more into it. Exactly one delta reports the end, and it is the one
// a consumer sees last.
//
// See https://docs.aws.amazon.com/bedrock/ for the full reference.
package bedrock
