package mcp

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"path"
	"slices"
	"strings"

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

func (c contentKind) admits(part chat.PartKind) bool {
	switch c {
	case contentText:
		return part == chat.PartText
	case contentImage, contentAudio, contentLink:
		return part == chat.PartMedia
	case contentResource:
		return part == chat.PartText || part == chat.PartMedia
	default:
		return false
	}
}

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

func (r resourceEnvelope) contents(part chat.ToolContent) (*sdkmcp.ResourceContents, error) {
	resource := &sdkmcp.ResourceContents{URI: r.URI, Meta: metadataToMCP(r.Meta)}
	if part.Kind == chat.PartText {
		resource.Text, resource.MIMEType = part.Text, r.MIMEType
		return resource, nil
	}
	inline := part.Media.Source.Kind == media.SourceBytes
	if r.MIMEType != "" || (!inline && r.URI != "") {
		return nil, errors.New("mcp: resource metadata duplicates a Core media field")
	}
	resource.MIMEType = part.Media.MIME
	var err error
	if inline {
		resource.Blob, err = part.Media.Bytes()
	} else {
		resource.URI, err = part.Media.URI()
	}
	if err != nil {
		return nil, err
	}
	return resource, nil
}

type coreContentEnvelope struct {
	Metadata  metadata.Map    `json:"metadata,omitzero"`
	Citations []chat.Citation `json:"citations,omitempty"`
}

func mapRemoteContent(content sdkmcp.Content) (chat.ToolContent, bool, error) {
	if lo.IsNil(content) {
		return chat.ToolContent{}, false, errors.New("mcp: content is nil")
	}
	if text, ok := content.(*sdkmcp.TextContent); ok && text.Text == "" {
		if len(text.Meta) != 0 || text.Annotations != nil {
			return chat.ToolContent{}, false, errors.New("mcp: empty text cannot carry content metadata")
		}
		return chat.ToolContent{}, false, nil
	}
	part, envelope, nativeMeta, err := splitRemoteContent(content)
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

func splitRemoteContent(content sdkmcp.Content) (chat.ToolContent, contentEnvelope, sdkmcp.Meta, error) {
	switch value := content.(type) {
	case *sdkmcp.TextContent:
		part := chat.ToolContent{Kind: chat.PartText, Text: value.Text}
		return part, contentEnvelope{Kind: contentText, Annotations: value.Annotations}, value.Meta, nil
	case *sdkmcp.ImageContent:
		part, err := remoteBytesMedia(value.MIMEType, value.Data)
		return part, contentEnvelope{Kind: contentImage, Annotations: value.Annotations}, value.Meta, err
	case *sdkmcp.AudioContent:
		part, err := remoteBytesMedia(value.MIMEType, value.Data)
		return part, contentEnvelope{Kind: contentAudio, Annotations: value.Annotations}, value.Meta, err
	case *sdkmcp.ResourceLink:
		part, err := remoteURIMedia(value.MIMEType, value.URI)
		if err == nil {
			part.Media.Name = value.Name
		}
		envelope := contentEnvelope{Kind: contentLink, Annotations: value.Annotations, Title: value.Title,
			Description: value.Description, Size: value.Size, Icons: value.Icons}
		return part, envelope, value.Meta, err
	case *sdkmcp.EmbeddedResource:
		part, envelope, err := splitRemoteResource(value)
		return part, envelope, value.Meta, err
	default:
		return chat.ToolContent{}, contentEnvelope{}, nil, fmt.Errorf("mcp: unsupported content %T", content)
	}
}

func splitRemoteResource(value *sdkmcp.EmbeddedResource) (chat.ToolContent, contentEnvelope, error) {
	resource := value.Resource
	if resource == nil {
		return chat.ToolContent{}, contentEnvelope{}, errors.New("mcp: embedded resource is nil")
	}
	resourceMeta, err := metadataFromMCP(resource.Meta)
	if err != nil {
		return chat.ToolContent{}, contentEnvelope{}, err
	}
	envelope := contentEnvelope{Kind: contentResource, Annotations: value.Annotations, Resource: &resourceEnvelope{Meta: resourceMeta}}
	var part chat.ToolContent
	switch {
	case resource.Text != "" && len(resource.Blob) != 0:
		return chat.ToolContent{}, contentEnvelope{}, errors.New("mcp: embedded resource carries both text and blob")
	case resource.Text != "":
		part = chat.ToolContent{Kind: chat.PartText, Text: resource.Text}
		envelope.Resource.URI, envelope.Resource.MIMEType = resource.URI, resource.MIMEType
	case len(resource.Blob) != 0:
		part, err = remoteBytesMedia(resourceMIME(resource.MIMEType, resource.URI), resource.Blob)
		envelope.Resource.URI = resource.URI
	case resource.URI != "":
		part, err = remoteURIMedia(resource.MIMEType, resource.URI)
	default:
		return chat.ToolContent{}, contentEnvelope{}, errors.New("mcp: embedded resource has no text, blob, or URI")
	}
	return part, envelope, err
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

const defaultResourceMIME = "application/octet-stream"

func resourceMIME(mimeType, uri string) string {
	if mimeType != "" {
		return mimeType
	}
	if parsed, err := url.Parse(uri); err == nil {
		if inferred := mime.TypeByExtension(path.Ext(parsed.Path)); inferred != "" {
			return inferred
		}
	}
	return defaultResourceMIME
}

func remoteURIMedia(mimeType, uri string) (chat.ToolContent, error) {
	value, err := media.NewURI(resourceMIME(mimeType, uri), uri)
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
		return mapServerMedia(part.Media, meta)
	}
	if err := c.validateFields(); err != nil {
		return nil, err
	}
	if !c.Kind.admits(part.Kind) {
		return nil, fmt.Errorf("mcp: content kind %q does not match Core part %q", c.Kind, part.Kind)
	}
	switch c.Kind {
	case contentText:
		return &sdkmcp.TextContent{Text: part.Text, Meta: meta, Annotations: c.Annotations}, nil
	case contentImage, contentAudio:
		return c.bytesContent(part.Media, meta)
	case contentLink:
		uri, err := part.Media.URI()
		if err != nil {
			return nil, err
		}
		return &sdkmcp.ResourceLink{URI: uri, Name: part.Media.Name, MIMEType: part.Media.MIME,
			Title: c.Title, Description: c.Description, Size: c.Size, Icons: c.Icons, Meta: meta, Annotations: c.Annotations}, nil
	default:
		resource, err := c.Resource.contents(part)
		if err != nil {
			return nil, err
		}
		return &sdkmcp.EmbeddedResource{Resource: resource, Meta: meta, Annotations: c.Annotations}, nil
	}
}

func (c contentEnvelope) validateFields() error {
	if c.Kind == contentResource && c.Resource == nil {
		return errors.New("mcp: embedded resource metadata is missing")
	}
	if c.Kind != contentResource && c.Resource != nil {
		return errors.New("mcp: content metadata has an unexpected resource")
	}
	if c.Kind != contentLink && (c.Title != "" || c.Description != "" || c.Size != nil || len(c.Icons) != 0) {
		return errors.New("mcp: content metadata has unexpected resource link fields")
	}
	return nil
}

func (c contentEnvelope) bytesContent(value *media.Media, meta sdkmcp.Meta) (sdkmcp.Content, error) {
	data, err := value.Bytes()
	if err != nil {
		return nil, err
	}
	if c.Kind == contentImage {
		return &sdkmcp.ImageContent{MIMEType: value.MIME, Data: data, Meta: meta, Annotations: c.Annotations}, nil
	}
	return &sdkmcp.AudioContent{MIMEType: value.MIME, Data: data, Meta: meta, Annotations: c.Annotations}, nil
}

// inlineResourceURIPrefix names opaque inline bytes, which MCP can carry only as
// an embedded resource and every resource requires a URI.
const inlineResourceURIPrefix = "scope://tool-output/"

func mapServerMedia(value *media.Media, meta sdkmcp.Meta) (sdkmcp.Content, error) {
	mediaType, _, err := mime.ParseMediaType(value.MIME)
	if err != nil {
		return nil, err
	}
	switch value.Source.Kind {
	case media.SourceBytes:
		data, err := value.Bytes()
		if err != nil {
			return nil, err
		}
		switch {
		case strings.HasPrefix(mediaType, "image/"):
			return &sdkmcp.ImageContent{MIMEType: value.MIME, Data: data, Meta: meta}, nil
		case strings.HasPrefix(mediaType, "audio/"):
			return &sdkmcp.AudioContent{MIMEType: value.MIME, Data: data, Meta: meta}, nil
		default:
			return &sdkmcp.EmbeddedResource{Meta: meta, Resource: &sdkmcp.ResourceContents{
				URI: inlineResourceURIPrefix + url.PathEscape(value.Name), MIMEType: value.MIME, Blob: data,
			}}, nil
		}
	case media.SourceURI:
		uri, err := value.URI()
		if err != nil {
			return nil, err
		}
		return &sdkmcp.ResourceLink{URI: uri, Name: value.Name, MIMEType: value.MIME, Meta: meta}, nil
	case media.SourceReference:
		reference, err := value.Reference()
		if err != nil {
			return nil, err
		}
		return &sdkmcp.ResourceLink{URI: reference, Name: value.Name, MIMEType: value.MIME, Meta: meta}, nil
	default:
		return nil, media.ErrInvalidSource
	}
}
