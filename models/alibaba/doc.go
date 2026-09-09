// Package alibaba wraps Alibaba Cloud's DashScope model platform,
// which hosts the Qwen / Tongyi family and several other Alibaba models.
//
// DashScope exposes two surfaces:
//   - the native /api/v1/services/aigc/text-generation/generation
//     endpoint with a DashScope-specific JSON shape (not used here);
//   - the /compatible-mode/v1 path which speaks the OpenAI
//     chat-completions / embeddings spec.
//
// This package uses the compatible-mode endpoint to route
// through the [openai] provider facade. DashScope-specific knobs
// (enable_thinking, enable_search, web search citations, etc.) use the
// namespaced OpenAI request extension.
//
// Compatible mode is not OpenAI's full surface, and DashScope's own parameter
// table is where the differences live: temperature is documented as [0, 2)
// rather than [0, 2], top_p as (0, 1.0), presence_penalty as "supported only
// on Qwen commercial models and open-source models qwen1.5 and later", and n
// as 1-4 on qwen-plus alone. The table also omits frequency_penalty,
// response_format and tool_choice, but omission is not a statement that they
// are discarded, so this package refuses nothing on that basis and leaves the
// answer to DashScope.
//
// See https://help.aliyun.com/zh/model-studio/ for the docs.
package alibaba
