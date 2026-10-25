// Package perplexity implements Perplexity's Agent API using the official
// OpenAI Responses SDK integration at https://api.perplexity.ai/v1/responses.
//
// Configure one model through chat.Options.Model. Effort-only selection and the
// native models fallback chain are outside this single-model binding. The
// optional RequestOptions extension selects agent effort, step budget, retrieval storage, and native
// built-in tools. Model reasoning effort, output format, and custom function
// tools remain owned by Core. Complete native terminal output, including search
// results and costs, is preserved under ResponseExtensionKey.
//
// Sonar Chat Completions support ended on September 27, 2026. This package has
// no Sonar endpoint or parameter conversion. See
// https://docs.perplexity.ai/docs/agent-api/openai-compatibility and
// https://docs.perplexity.ai/docs/agent-api/migrate-from-sonar/overview.
package perplexity
