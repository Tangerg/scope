package luma

import (
	"encoding/base64"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"mime"
	"strings"

	lumaagents "github.com/lumalabs/luma-agents-go"
)

const (
	maximumImageReferences     = 9
	maximumImageEditReferences = 8
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

func (i ImageRequestOptions) Validate() error {
	maximumReferences := maximumImageReferences
	switch lumaagents.GenerationNewParamsType(i.Type) {
	case "", lumaagents.GenerationNewParamsTypeImage:
		if i.Source != nil {
			return errors.New("luma: source requires type image_edit")
		}
	case lumaagents.GenerationNewParamsTypeImageEdit:
		maximumReferences = maximumImageEditReferences
		if i.Source == nil {
			return errors.New("luma: image_edit requires source")
		}
		if err := i.Source.Validate(); err != nil {
			return fmt.Errorf("luma: source: %w", err)
		}
	default:
		return fmt.Errorf("luma: type %q is not an image operation", i.Type)
	}
	if len(i.ImageRef) > maximumReferences {
		return fmt.Errorf("luma: image_ref has %d references; maximum is %d", len(i.ImageRef), maximumReferences)
	}
	for index := range i.ImageRef {
		if err := i.ImageRef[index].Validate(); err != nil {
			return fmt.Errorf("luma: image_ref[%d]: %w", index, err)
		}
	}
	return nil
}

// apply sets the native controls these options carry on params.
func (i ImageRequestOptions) apply(params *lumaagents.GenerationNewParams) {
	if i.Type != "" {
		params.Type = lumaagents.F(lumaagents.GenerationNewParamsType(i.Type))
	}
	if i.AspectRatio != "" {
		params.AspectRatio = lumaagents.F(lumaagents.GenerationNewParamsAspectRatio(i.AspectRatio))
	}
	if i.Source != nil {
		params.Source = lumaagents.F(i.Source.sdkParams())
	}
	if i.ImageRef != nil {
		references := make([]lumaagents.ImageRefParam, len(i.ImageRef))
		for index, reference := range i.ImageRef {
			references[index] = reference.sdkParams()
		}
		params.ImageRef = lumaagents.F(references)
	}
	if i.Style != "" {
		params.Style = lumaagents.F(lumaagents.GenerationNewParamsStyle(i.Style))
	}
	if i.UserID != "" {
		params.UserID = lumaagents.F(i.UserID)
	}
	if i.WebSearch != nil {
		params.WebSearch = lumaagents.F(*i.WebSearch)
	}
}

func (i *ImageRequestOptions) UnmarshalJSON(data []byte) error {
	type wireOptions ImageRequestOptions
	var decoded *wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("luma: decode image options: %w", err)
	}
	if decoded == nil {
		return errors.New("luma: image options must be an object")
	}
	candidate := ImageRequestOptions(*decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*i = candidate
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

func (i ImageReference) Validate() error {
	identities := 0
	for _, identity := range []string{i.Data, i.FileID, i.GenerationID, i.URL} {
		if identity != "" {
			identities++
		}
	}
	if identities != 1 {
		return errors.New("image reference requires exactly one of data, file_id, generation_id, or url")
	}
	if i.Data == "" {
		if i.MediaType != "" {
			return errors.New("media_type requires inline data")
		}
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(i.MediaType)
	if err != nil || !strings.HasPrefix(mediaType, mediaTypeImagePrefix) {
		return errors.New("inline image data requires an image media_type")
	}
	if _, err := base64.StdEncoding.DecodeString(i.Data); err != nil {
		return fmt.Errorf("inline image data must be base64: %w", err)
	}
	return nil
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
