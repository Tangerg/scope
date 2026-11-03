package alibaba

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "alibaba"
)

const (
	// BaseURLChina is the domestic DashScope OpenAI-compat endpoint.
	BaseURLChina = "https://dashscope.aliyuncs.com/compatible-mode/v1"

	// BaseURLIntl is the Singapore DashScope OpenAI-compat endpoint —
	// required for international (non-mainland-China) users.
	BaseURLIntl = "https://dashscope-intl.aliyuncs.com/compatible-mode/v1"
)

// Current chat model ids. The Qwen family is versioned aggressively; the
// constants below intentionally name rolling production aliases. See
// https://help.aliyun.com/zh/model-studio/getting-started/models for
// the live catalog.
const (
	ModelQwen37Max   = "qwen3.7-max"
	ModelQwen37Plus  = "qwen3.7-plus"
	ModelQwen36Plus  = "qwen3.6-plus"
	ModelQwen36Flash = "qwen3.6-flash"

	// ModelQwen3CoderPlus (qwen3-coder-plus) targets code generation
	// / completion / repair.
	ModelQwen3CoderPlus = "qwen3-coder-plus"
)

// Embedding model ids.
const (
	// ModelEmbeddingV4 (text-embedding-v4) is the current general-purpose
	// embedding model.
	ModelEmbeddingV4 = "text-embedding-v4"
)
