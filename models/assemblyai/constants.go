package assemblyai

import (
	"time"
)

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "AssemblyAI"
)

// Exported identifiers keep provider-owned names and defaults out of caller literals.
const (
	RequestExtensionKey  = "assemblyai/request"
	ResponseExtensionKey = "assemblyai/response"

	// DefaultBaseURL is AssemblyAI's production REST endpoint.
	DefaultBaseURL = "https://api.assemblyai.com/v2"

	// DefaultPollInterval is how often [TranscriptionModel.Call]
	// re-checks a queued job. AssemblyAI's typical real-time-factor is
	// 0.1–0.3x audio length so 2s strikes a balance between latency and
	// API call volume.
	DefaultPollInterval = 2 * time.Second

	// DefaultPollTimeout caps the total wait for one Call. Long audio
	// (hour-scale lectures, podcasts) plus model warm-up can take a
	// while, so the ceiling is set generously; callers wanting a
	// tighter SLA should pass a ctx with their own deadline.
	DefaultPollTimeout = 30 * time.Minute
)

// These are the provider values this adapter recognizes.
const (
	ModelUniversal3Point5Pro = "universal-3-5-pro"
	ModelUniversal2          = "universal-2"
)
