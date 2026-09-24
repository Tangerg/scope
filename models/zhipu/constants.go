package zhipu

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "Zhipu"
)

const (
	// BaseURL is the BigModel v4 OpenAI-compatible endpoint.
	BaseURL = "https://open.bigmodel.cn/api/paas/v4"

	// BaseURLAnthropic is the Anthropic-compatible endpoint Zhipu
	// exposes for GLM-4.5 / GLM-4.6. The anthropic-sdk-go client
	// appends "v1/messages" so the full URL resolves to
	// https://open.bigmodel.cn/api/anthropic/v1/messages.
	BaseURLAnthropic = "https://open.bigmodel.cn/api/anthropic"
)

// Current chat model ids. See
// https://docs.bigmodel.cn/cn/guide/start/model-overview.
const (
	ModelGLM52       = "glm-5.2"
	ModelGLM47       = "glm-4.7"
	ModelGLM47FlashX = "glm-4.7-flashx"
)

// Embedding model ids.
const (
	// ModelEmbedding3 produces 2048-dim vectors by default; the
	// output_dimension parameter (passed through embedding.Options.Dimensions)
	// can truncate down to 256 / 512 / 1024.
	ModelEmbedding3 = "embedding-3"
)
