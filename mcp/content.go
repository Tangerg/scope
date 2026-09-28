package mcp

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

// ContentMetadataKey identifies MCP content annotations and resource provenance
// in Core Part and ToolContent metadata. Text and media stay in their Core
// payload fields; this envelope never contains another copy of those payloads.
const ContentMetadataKey = "mcp/content"

const coreContentMetadataKey = "scope/content"

type contentKind string

const (
	contentText     contentKind = "text"
	contentImage    contentKind = "image"
	contentAudio    contentKind = "audio"
	contentLink     contentKind = "resource_link"
	contentResource contentKind = "resource"
)

type contentEnvelope struct {
	Kind        contentKind         `json:"type"`
	Meta        metadata.Map        `json:"_meta,omitzero"`
	Annotations *sdkmcp.Annotations `json:"annotations,omitzero"`
	Resource    *resourceEnvelope   `json:"resource,omitzero"`
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	Size        *int64              `json:"size,omitzero"`
	Icons       []sdkmcp.Icon       `json:"icons,omitempty"`
}

type resourceEnvelope struct {
	// URI is provenance for inline data; a linked resource uses Core Media.URI.
	URI      string       `json:"uri,omitempty"`
	MIMEType string       `json:"mimeType,omitempty"`
	Meta     metadata.Map `json:"_meta,omitzero"`
}

type coreContentEnvelope struct {
	Metadata  metadata.Map    `json:"metadata,omitzero"`
	Citations []chat.Citation `json:"citations,omitempty"`
}

func mapRemoteContent(content sdkmcp.Content) (chat.ToolContent, bool, error) {
	if lo.IsNil(content) {
		return chat.ToolContent{}, false, errors.New("mcp: content is nil")
	}
	var part chat.ToolContent
	var envelope contentEnvelope
	var nativeMeta sdkmcp.Meta
	var err error
	switch value := content.(type) {
	case *sdkmcp.TextContent:
		if value.Text == "" {
			if len(value.Meta) != 0 || value.Annotations != nil {
				return chat.ToolContent{}, false, errors.New("mcp: empty text cannot carry content metadata")
			}
			return chat.ToolContent{}, false, nil
		}
		part = chat.ToolContent{Kind: chat.PartText, Text: value.Text}
		envelope.Kind, envelope.Annotations, nativeMeta = contentText, value.Annotations, value.Meta
	case *sdkmcp.ImageContent:
		part, err = remoteBytesMedia(value.MIMEType, value.Data)
		envelope.Kind, envelope.Annotations, nativeMeta = contentImage, value.Annotations, value.Meta
	case *sdkmcp.AudioContent:
		part, err = remoteBytesMedia(value.MIMEType, value.Data)
		envelope.Kind, envelope.Annotations, nativeMeta = contentAudio, value.Annotations, value.Meta
	case *sdkmcp.ResourceLink:
		var linked *media.Media
		linked, err = media.NewURI(resourceMIME(value.MIMEType, value.URI), value.URI)
		if err == nil {
			linked.Name = value.Name
			part = chat.ToolContent{Kind: chat.PartMedia, Media: linked}
		}
		envelope = contentEnvelope{Kind: contentLink, Annotations: value.Annotations, Title: value.Title,
			Description: value.Description, Size: value.Size, Icons: value.Icons}
		nativeMeta = value.Meta
	case *sdkmcp.EmbeddedResource:
		if value.Resource == nil {
			return chat.ToolContent{}, false, errors.New("mcp: embedded resource is nil")
		}
		resource := value.Resource
		envelope = contentEnvelope{Kind: contentResource, Annotations: value.Annotations, Resource: &resourceEnvelope{}}
		envelope.Resource.Meta, err = metadataFromMCP(resource.Meta)
		if err != nil {
			return chat.ToolContent{}, false, err
		}
		switch {
		case resource.Text != "":
			if len(resource.Blob) != 0 {
				return chat.ToolContent{}, false, errors.New("mcp: embedded resource carries both text and blob")
			}
			part = chat.ToolContent{Kind: chat.PartText, Text: resource.Text}
			envelope.Resource.URI, envelope.Resource.MIMEType = resource.URI, resource.MIMEType
		case len(resource.Blob) != 0:
			part, err = remoteBytesMedia(resourceMIME(resource.MIMEType, resource.URI), resource.Blob)
			envelope.Resource.URI = resource.URI
		case resource.URI != "":
			var linked *media.Media
			linked, err = media.NewURI(resourceMIME(resource.MIMEType, resource.URI), resource.URI)
			part = chat.ToolContent{Kind: chat.PartMedia, Media: linked}
		default:
			return chat.ToolContent{}, false, errors.New("mcp: embedded resource has no text, blob, or URI")
		}
		nativeMeta = value.Meta
	default:
		return chat.ToolContent{}, false, fmt.Errorf("mcp: unsupported content %T", content)
	}
	if err != nil {
		return chat.ToolContent{}, false, err
	}
	if err := envelope.attach(&part, nativeMeta); err != nil {
		return chat.ToolContent{}, false, err
	}
	if err := part.Validate(); err != nil {
		return chat.ToolContent{}, false, fmt.Errorf("mcp: mapped content: %w", err)
	}
	return part, true, nil
}

func (c contentEnvelope) attach(part *chat.ToolContent, nativeMeta sdkmcp.Meta) error {
	var err error
	c.Meta, err = metadataFromMCP(nativeMeta)
	if err != nil {
		return err
	}
	if raw, present := c.Meta[coreContentMetadataKey]; present {
		var encoded string
		if err := jsonv2.Unmarshal(raw, &encoded); err != nil {
			return fmt.Errorf("mcp: decode Core content metadata encoding: %w", err)
		}
		var portable coreContentEnvelope
		if err := jsonv2.Unmarshal([]byte(encoded), &portable, jsonv2.RejectUnknownMembers(true)); err != nil {
			return fmt.Errorf("mcp: decode Core content metadata: %w", err)
		}
		if _, collision := portable.Metadata[ContentMetadataKey]; collision {
			return errors.New("mcp: Core content metadata contains a nested MCP envelope")
		}
		part.Metadata, part.Citations = portable.Metadata, portable.Citations
		delete(c.Meta, coreContentMetadataKey)
	}
	if c.Kind == contentText && len(c.Meta) == 0 && c.Annotations == nil {
		return nil
	}
	return part.Metadata.Set(ContentMetadataKey, c)
}

func metadataFromMCP(native sdkmcp.Meta) (metadata.Map, error) {
	var values metadata.Map
	for key, value := range native {
		if err := values.Set(key, value); err != nil {
			return nil, fmt.Errorf("mcp: encode content metadata: %w", err)
		}
	}
	return values, nil
}

func metadataToMCP(values metadata.Map) sdkmcp.Meta {
	if len(values) == 0 {
		return nil
	}
	native := make(sdkmcp.Meta, len(values))
	for key, value := range values {
		native[key] = json.RawMessage(slices.Clone(value))
	}
	return native
}

func remoteBytesMedia(mimeType string, data []byte) (chat.ToolContent, error) {
	value, err := media.NewBytes(mimeType, data)
	if err != nil {
		return chat.ToolContent{}, err
	}
	return chat.ToolContent{Kind: chat.PartMedia, Media: value}, nil
}

func mapServerContent(part chat.ToolContent) (sdkmcp.Content, error) {
	portable := coreContentEnvelope{Metadata: part.Metadata.Clone(), Citations: slices.Clone(part.Citations)}
	var envelope contentEnvelope
	if raw, present := portable.Metadata[ContentMetadataKey]; present {
		if err := jsonv2.Unmarshal(raw, &envelope, jsonv2.RejectUnknownMembers(true)); err != nil {
			return nil, fmt.Errorf("mcp: decode content metadata: %w", err)
		}
		if envelope.Kind == "" {
			return nil, errors.New("mcp: content metadata kind is missing")
		}
		delete(portable.Metadata, ContentMetadataKey)
	}
	if _, collision := envelope.Meta[coreContentMetadataKey]; collision {
		return nil, errors.New("mcp: content metadata uses the reserved Core metadata key")
	}
	if len(portable.Metadata) != 0 || len(portable.Citations) != 0 {
		encoded, err := jsonv2.Marshal(portable)
		if err != nil {
			return nil, err
		}
		// JSON text keeps exact Core numbers intact through SDKs that decode
		// arbitrary protocol metadata into floating-point interface values.
		if err := envelope.Meta.Set(coreContentMetadataKey, string(encoded)); err != nil {
			return nil, err
		}
	}
	return envelope.content(part)
}

func (c contentEnvelope) content(part chat.ToolContent) (sdkmcp.Content, error) {
	meta := metadataToMCP(c.Meta)
	if c.Kind == "" {
		if part.Kind == chat.PartText {
			return &sdkmcp.TextContent{Text: part.Text, Meta: meta}, nil
		}
		content, err := mapServerMedia(part.Media)
		if err != nil {
			return nil, err
		}
		switch value := content.(type) {
		case *sdkmcp.ImageContent:
			value.Meta = meta
		case *sdkmcp.AudioContent:
			value.Meta = meta
		case *sdkmcp.ResourceLink:
			value.Meta = meta
		case *sdkmcp.EmbeddedResource:
			value.Meta = meta
		}
		return content, nil
	}
	if c.Kind != contentResource && c.Resource != nil {
		return nil, errors.New("mcp: content metadata has an unexpected resource")
	}
	if c.Kind != contentLink && (c.Title != "" || c.Description != "" || c.Size != nil || len(c.Icons) != 0) {
		return nil, errors.New("mcp: content metadata has unexpected resource link fields")
	}
	switch c.Kind {
	case contentText:
		if part.Kind == chat.PartText {
			return &sdkmcp.TextContent{Text: part.Text, Meta: meta, Annotations: c.Annotations}, nil
		}
	case contentImage, contentAudio:
		if part.Kind == chat.PartMedia {
			data, err := part.Media.Bytes()
			if err != nil {
				return nil, err
			}
			if c.Kind == contentImage {
				return &sdkmcp.ImageContent{MIMEType: part.Media.MIME, Data: data, Meta: meta, Annotations: c.Annotations}, nil
			}
			return &sdkmcp.AudioContent{MIMEType: part.Media.MIME, Data: data, Meta: meta, Annotations: c.Annotations}, nil
		}
	case contentLink:
		if part.Kind == chat.PartMedia {
			uri, err := part.Media.URI()
			if err != nil {
				return nil, err
			}
			return &sdkmcp.ResourceLink{URI: uri, Name: part.Media.Name, MIMEType: part.Media.MIME,
				Title: c.Title, Description: c.Description, Size: c.Size, Icons: c.Icons, Meta: meta, Annotations: c.Annotations}, nil
		}
	case contentResource:
		if c.Resource == nil {
			return nil, errors.New("mcp: embedded resource metadata is missing")
		}
		resource := &sdkmcp.ResourceContents{URI: c.Resource.URI, Meta: metadataToMCP(c.Resource.Meta)}
		if part.Kind == chat.PartText {
			resource.Text, resource.MIMEType = part.Text, c.Resource.MIMEType
		} else {
			if c.Resource.MIMEType != "" || (part.Media.Source.Kind != media.SourceBytes && c.Resource.URI != "") {
				return nil, errors.New("mcp: resource metadata duplicates a Core media field")
			}
			resource.MIMEType = part.Media.MIME
			var err error
			if part.Media.Source.Kind == media.SourceBytes {
				resource.Blob, err = part.Media.Bytes()
			} else {
				resource.URI, err = part.Media.URI()
			}
			if err != nil {
				return nil, err
			}
		}
		return &sdkmcp.EmbeddedResource{Resource: resource, Meta: meta, Annotations: c.Annotations}, nil
	}
	return nil, fmt.Errorf("mcp: content kind %q does not match Core part %q", c.Kind, part.Kind)
}
