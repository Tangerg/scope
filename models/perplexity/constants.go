package perplexity

const (
	Provider = "Perplexity"

	BaseURL = "https://api.perplexity.ai"
)

// Sonar online-search models. See https://docs.perplexity.ai/guides/model-cards
// for the current catalog. ModelSonarDeepResearch runs an extended
// multi-hop research loop — slow, expensive, produces deeply-cited
// reports.
const (
	ModelSonar             = "sonar"
	ModelSonarPro          = "sonar-pro"
	ModelSonarReasoningPro = "sonar-reasoning-pro"
	ModelSonarDeepResearch = "sonar-deep-research"
)
