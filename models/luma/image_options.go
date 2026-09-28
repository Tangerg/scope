package luma

import (
	jsonv2 "encoding/json/v2"
	"fmt"

	lumaagents "github.com/lumalabs/luma-agents-go"
)

// ImageRequestOptions carries Luma image controls under ImageRequestExtensionKey.
// Core owns prompt, model, and output format. Type defaults to image; image_edit
// accepts Source as the image to edit. Video and layering use other protocols.
type ImageRequestOptions struct {
	AspectRatio string           `json:"aspect_ratio,omitempty"`
	ImageRef    []ImageReference `json:"image_ref,omitzero"`
	Source      *ImageReference  `json:"source,omitzero"`
	Style       string           `json:"style,omitempty"`
	Type        string           `json:"type,omitempty"`
	UserID      string           `json:"user_id,omitempty"`
	WebSearch   *bool            `json:"web_search,omitzero"`
}

func (i *ImageRequestOptions) UnmarshalJSON(data []byte) error {
	type wireOptions ImageRequestOptions
	var decoded wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("luma: decode image options: %w", err)
	}
	*i = ImageRequestOptions(decoded)
	return nil
}

// ImageReference identifies one reference through URL, inline base64 Data,
// GenerationID, or FileID. MediaType describes inline data when used.
type ImageReference struct {
	Data         string `json:"data,omitempty"`
	FileID       string `json:"file_id,omitempty"`
	GenerationID string `json:"generation_id,omitempty"`
	MediaType    string `json:"media_type,omitempty"`
	URL          string `json:"url,omitempty"`
}

func (i ImageReference) sdkParams() lumaagents.ImageRefParam {
	var value lumaagents.ImageRefParam
	if i.Data != "" {
		value.Data = lumaagents.F(i.Data)
	}
	if i.FileID != "" {
		value.FileID = lumaagents.F(i.FileID)
	}
	if i.GenerationID != "" {
		value.GenerationID = lumaagents.F(i.GenerationID)
	}
	if i.MediaType != "" {
		value.MediaType = lumaagents.F(i.MediaType)
	}
	if i.URL != "" {
		value.URL = lumaagents.F(i.URL)
	}
	return value
}
