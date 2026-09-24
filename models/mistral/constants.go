package mistral

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "Mistral"
)

// Exported defaults keep constructor behavior visible and overridable.
const (
	DefaultBaseURL = "https://api.mistral.ai/v1"
)

// Current chat model ids. See https://docs.mistral.ai/models/.
const (
	// ModelMedium is the current frontier-class model with adjustable reasoning.
	ModelMedium = "mistral-medium-latest"

	// ModelSmall is the current hybrid instruct, reasoning, and coding model.
	ModelSmall = "mistral-small-2603"

	// ModelLarge is Mistral Large 3.
	ModelLarge = "mistral-large-2512"

	// ModelCodestral targets code generation and fill-in-the-middle.
	ModelCodestral = "codestral-latest"

	ModelMinistral3B = "ministral-3b-latest"
	ModelMinistral8B = "ministral-8b-latest"

	// ModelPixtralLarge remains the supported Pixtral Large model.
	ModelPixtralLarge = "pixtral-large-latest"
)

// Embedding model ids.
const (
	// ModelEmbed (mistral-embed) is the general-purpose embedding
	// model. 1024-dim by default; pass [embedding.Options.Dimensions]
	// to truncate.
	ModelEmbed = "mistral-embed"

	// ModelCodestralEmbed is the code-tuned
	// embedding model.
	ModelCodestralEmbed = "codestral-embed-2505"
)

// Moderation model ids.
const (
	// ModelModeration is the current moderation
	// classifier reachable via [NewModerationModel].
	ModelModeration = "mistral-moderation-2603"
)
