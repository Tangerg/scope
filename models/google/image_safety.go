package google

import "fmt"

type ImageSafetySetting struct {
	Type      ImageSafetyCategory  `json:"type"`
	Threshold ImageSafetyThreshold `json:"threshold"`
	Method    ImageSafetyMethod    `json:"method,omitempty"`
}

func (i ImageSafetySetting) validate() error {
	switch {
	case !i.Type.valid():
		return fmt.Errorf("unsupported safety category %q", i.Type)
	case !i.Threshold.valid():
		return fmt.Errorf("unsupported safety threshold %q", i.Threshold)
	case !i.Method.valid():
		return fmt.Errorf("unsupported safety method %q", i.Method)
	default:
		return nil
	}
}

type ImageSafetyCategory string

const (
	ImageSafetyCategoryHateSpeech            ImageSafetyCategory = "hate_speech"
	ImageSafetyCategoryDangerousContent      ImageSafetyCategory = "dangerous_content"
	ImageSafetyCategoryHarassment            ImageSafetyCategory = "harassment"
	ImageSafetyCategorySexuallyExplicit      ImageSafetyCategory = "sexually_explicit"
	ImageSafetyCategoryImageHate             ImageSafetyCategory = "image_hate"
	ImageSafetyCategoryImageDangerousContent ImageSafetyCategory = "image_dangerous_content"
	ImageSafetyCategoryImageHarassment       ImageSafetyCategory = "image_harassment"
	ImageSafetyCategoryImageSexuallyExplicit ImageSafetyCategory = "image_sexually_explicit"
	ImageSafetyCategoryJailbreak             ImageSafetyCategory = "jailbreak"
)

func (i ImageSafetyCategory) valid() bool {
	switch i {
	case ImageSafetyCategoryHateSpeech, ImageSafetyCategoryDangerousContent, ImageSafetyCategoryHarassment, ImageSafetyCategorySexuallyExplicit, ImageSafetyCategoryImageHate, ImageSafetyCategoryImageDangerousContent, ImageSafetyCategoryImageHarassment, ImageSafetyCategoryImageSexuallyExplicit, ImageSafetyCategoryJailbreak:
		return true
	default:
		return false
	}
}

type ImageSafetyThreshold string

const (
	ImageSafetyThresholdBlockLowAndAbove    ImageSafetyThreshold = "block_low_and_above"
	ImageSafetyThresholdBlockMediumAndAbove ImageSafetyThreshold = "block_medium_and_above"
	ImageSafetyThresholdBlockOnlyHigh       ImageSafetyThreshold = "block_only_high"
	ImageSafetyThresholdBlockNone           ImageSafetyThreshold = "block_none"
	ImageSafetyThresholdOff                 ImageSafetyThreshold = "off"
)

func (i ImageSafetyThreshold) valid() bool {
	switch i {
	case ImageSafetyThresholdBlockLowAndAbove, ImageSafetyThresholdBlockMediumAndAbove, ImageSafetyThresholdBlockOnlyHigh, ImageSafetyThresholdBlockNone, ImageSafetyThresholdOff:
		return true
	default:
		return false
	}
}

type ImageSafetyMethod string

const (
	ImageSafetyMethodSeverity    ImageSafetyMethod = "severity"
	ImageSafetyMethodProbability ImageSafetyMethod = "probability"
)

func (i ImageSafetyMethod) valid() bool {
	switch i {
	case "", ImageSafetyMethodSeverity, ImageSafetyMethodProbability:
		return true
	default:
		return false
	}
}
