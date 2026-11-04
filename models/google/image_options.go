package google

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/media"
)

// ImageGenerationOptions carries the current Interactions API controls that
// do not have provider-neutral equivalents in [image.Options]. Store it under
// [ImageRequestExtensionKey].
type ImageGenerationOptions struct {
	AspectRatio           ImageAspectRatio          `json:"aspect_ratio,omitempty"`
	ImageSize             ImageSize                 `json:"image_size,omitempty"`
	Delivery              ImageDelivery             `json:"delivery,omitempty"`
	PreviousInteractionID string                    `json:"previous_interaction_id,omitempty"`
	Store                 *bool                     `json:"store,omitzero"`
	ThinkingLevel         ImageThinkingLevel        `json:"thinking_level,omitempty"`
	ThinkingSummaries     ImageThinkingSummaryMode  `json:"thinking_summaries,omitempty"`
	ServiceTier           ImageServiceTier          `json:"service_tier,omitempty"`
	Labels                map[string]string         `json:"labels,omitempty"`
	InputImages           []*media.Media            `json:"input_images,omitempty"`
	GoogleSearch          *ImageGoogleSearchOptions `json:"google_search,omitzero"`
	SafetySettings        []ImageSafetySetting      `json:"safety_settings,omitempty"`
}

func (i *ImageGenerationOptions) UnmarshalJSON(data []byte) error {
	type wireOptions ImageGenerationOptions
	var decoded *wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return err
	}
	if decoded == nil {
		return errors.New("google: image options must be an object")
	}
	*i = ImageGenerationOptions(*decoded)
	return nil
}

func (i *ImageGenerationOptions) validate(modelName string) error {
	if i == nil {
		return errors.New("google: image: nil provider options")
	}
	if err := i.validateValues(); err != nil {
		return err
	}
	if err := i.validateModel(modelName); err != nil {
		return err
	}
	for index, setting := range i.SafetySettings {
		if err := setting.validate(); err != nil {
			return fmt.Errorf("google: image: safety_settings[%d]: %w", index, err)
		}
	}
	return nil
}

func (i *ImageGenerationOptions) validateValues() error {
	switch {
	case !i.AspectRatio.valid():
		return fmt.Errorf("google: image: unsupported aspect ratio %q", i.AspectRatio)
	case !i.ImageSize.valid():
		return fmt.Errorf("google: image: unsupported image size %q", i.ImageSize)
	case !i.Delivery.valid():
		return fmt.Errorf("google: image: unsupported delivery %q", i.Delivery)
	case !i.ThinkingLevel.valid():
		return fmt.Errorf("google: image: unsupported thinking level %q", i.ThinkingLevel)
	case !i.ThinkingSummaries.valid():
		return fmt.Errorf("google: image: unsupported thinking summaries %q", i.ThinkingSummaries)
	case !i.ServiceTier.valid():
		return fmt.Errorf("google: image: unsupported service tier %q", i.ServiceTier)
	}
	return i.GoogleSearch.validate()
}

func (i *ImageGenerationOptions) validateModel(modelName string) error {
	switch modelName {
	case ModelGemini31FlashLiteImage:
		if i.ImageSize != "" && i.ImageSize != ImageSize1K {
			return fmt.Errorf("google: image: model %q only supports image size 1K", modelName)
		}
		if i.GoogleSearch != nil {
			return fmt.Errorf("google: image: model %q does not support Google Search grounding", modelName)
		}
		if i.ThinkingLevel != "" && i.ThinkingLevel != ImageThinkingLevelMinimal && i.ThinkingLevel != ImageThinkingLevelHigh {
			return fmt.Errorf("google: image: model %q only supports minimal or high thinking", modelName)
		}
	case ModelGemini25FlashImage:
		if i.ImageSize != "" && i.ImageSize != ImageSize1K {
			return fmt.Errorf("google: image: model %q only supports image size 1K", modelName)
		}
	case ModelGemini3ProImage:
		if i.ImageSize == ImageSize512 {
			return fmt.Errorf("google: image: model %q does not support image size 512", modelName)
		}
	}
	return nil
}

type ImageGoogleSearchOptions struct {
	SearchTypes []ImageSearchType `json:"search_types,omitempty"`
}

func (i *ImageGoogleSearchOptions) validate() error {
	if i == nil {
		return nil
	}
	for index, searchType := range i.SearchTypes {
		if !searchType.valid() {
			return fmt.Errorf("google: image: google_search.search_types[%d]: unsupported value %q", index, searchType)
		}
	}
	return nil
}

type ImageAspectRatio string

const (
	ImageAspectRatio1x1  ImageAspectRatio = "1:1"
	ImageAspectRatio2x3  ImageAspectRatio = "2:3"
	ImageAspectRatio3x2  ImageAspectRatio = "3:2"
	ImageAspectRatio3x4  ImageAspectRatio = "3:4"
	ImageAspectRatio4x3  ImageAspectRatio = "4:3"
	ImageAspectRatio4x5  ImageAspectRatio = "4:5"
	ImageAspectRatio5x4  ImageAspectRatio = "5:4"
	ImageAspectRatio9x16 ImageAspectRatio = "9:16"
	ImageAspectRatio16x9 ImageAspectRatio = "16:9"
	ImageAspectRatio21x9 ImageAspectRatio = "21:9"
	ImageAspectRatio1x8  ImageAspectRatio = "1:8"
	ImageAspectRatio8x1  ImageAspectRatio = "8:1"
	ImageAspectRatio1x4  ImageAspectRatio = "1:4"
	ImageAspectRatio4x1  ImageAspectRatio = "4:1"
)

func (i ImageAspectRatio) valid() bool {
	switch i {
	case "", ImageAspectRatio1x1, ImageAspectRatio2x3, ImageAspectRatio3x2, ImageAspectRatio3x4, ImageAspectRatio4x3, ImageAspectRatio4x5, ImageAspectRatio5x4, ImageAspectRatio9x16, ImageAspectRatio16x9, ImageAspectRatio21x9, ImageAspectRatio1x8, ImageAspectRatio8x1, ImageAspectRatio1x4, ImageAspectRatio4x1:
		return true
	default:
		return false
	}
}

type ImageSize string

const (
	ImageSize512 ImageSize = "512"
	ImageSize1K  ImageSize = "1K"
	ImageSize2K  ImageSize = "2K"
	ImageSize4K  ImageSize = "4K"
)

func (i ImageSize) valid() bool {
	switch i {
	case "", ImageSize512, ImageSize1K, ImageSize2K, ImageSize4K:
		return true
	default:
		return false
	}
}

type ImageDelivery string

const (
	ImageDeliveryInline ImageDelivery = "inline"
	ImageDeliveryURI    ImageDelivery = "uri"
)

func (i ImageDelivery) valid() bool {
	switch i {
	case "", ImageDeliveryInline, ImageDeliveryURI:
		return true
	default:
		return false
	}
}

type ImageThinkingLevel string

const (
	ImageThinkingLevelMinimal ImageThinkingLevel = "minimal"
	ImageThinkingLevelLow     ImageThinkingLevel = "low"
	ImageThinkingLevelMedium  ImageThinkingLevel = "medium"
	ImageThinkingLevelHigh    ImageThinkingLevel = "high"
)

func (i ImageThinkingLevel) valid() bool {
	switch i {
	case "", ImageThinkingLevelMinimal, ImageThinkingLevelLow, ImageThinkingLevelMedium, ImageThinkingLevelHigh:
		return true
	default:
		return false
	}
}

type ImageThinkingSummaryMode string

const (
	ImageThinkingSummaryModeAuto ImageThinkingSummaryMode = "auto"
	ImageThinkingSummaryModeNone ImageThinkingSummaryMode = "none"
)

func (i ImageThinkingSummaryMode) valid() bool {
	switch i {
	case "", ImageThinkingSummaryModeAuto, ImageThinkingSummaryModeNone:
		return true
	default:
		return false
	}
}

type ImageServiceTier string

const (
	ImageServiceTierFlex     ImageServiceTier = "flex"
	ImageServiceTierStandard ImageServiceTier = "standard"
	ImageServiceTierPriority ImageServiceTier = "priority"
)

func (i ImageServiceTier) valid() bool {
	switch i {
	case "", ImageServiceTierFlex, ImageServiceTierStandard, ImageServiceTierPriority:
		return true
	default:
		return false
	}
}

type ImageSearchType string

const (
	ImageSearchTypeWeb   ImageSearchType = "web_search"
	ImageSearchTypeImage ImageSearchType = "image_search"
)

func (i ImageSearchType) valid() bool {
	switch i {
	case ImageSearchTypeWeb, ImageSearchTypeImage:
		return true
	default:
		return false
	}
}
