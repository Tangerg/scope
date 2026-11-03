package huggingface

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "huggingface"
)

const (
	// DefaultBaseURL targets the HuggingFace router which proxies to a
	// curated set of inference providers (together, fireworks, nebius,
	// sambanova, hf-inference, ...). The router exposes an
	// OpenAI-compatible /chat/completions endpoint.
	DefaultBaseURL = "https://router.huggingface.co/v1"
)

// ModelGPTOSS120B is the model identifier this adapter is verified against.
const ModelGPTOSS120B = "openai/gpt-oss-120b"
