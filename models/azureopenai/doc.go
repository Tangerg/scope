// Package azureopenai adapts Azure OpenAI's current OpenAI-compatible v1 API
// to the Core model interfaces.
//
// BaseURL is the complete Azure OpenAI v1 base URL, for example
// "https://RESOURCE.openai.azure.com/openai/v1/". Model names are Azure
// deployment names. Authentication uses the API key through the standard
// OpenAI client authentication path accepted by Azure's v1 endpoint.
//
// Only the current v1 endpoint shape is modeled; no dated api-version is
// required.
//
// Output token limits go out as max_completion_tokens. Azure documents that
// reasoning models "will only work with the max_completion_tokens parameter
// when using the Chat Completions API", and lists max_tokens among the
// parameters those models do not support, so max_tokens was the one field a
// caller's MaxOutputTokens could not survive on a GPT-5 or o-series
// deployment.
package azureopenai
