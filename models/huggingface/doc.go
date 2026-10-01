// Package huggingface exposes the HuggingFace Inference Router, which
// is OpenAI-compatible — chat completions hit /v1/chat/completions with
// the same request/response shape.
//
// [NewChatCompletions] returns the shared [openai.ChatCompletions] protocol
// model configured for the Hugging Face router, with tool calling and
// streaming.
package huggingface
