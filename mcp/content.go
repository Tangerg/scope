package mcp

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"
	"github.com/samber/lo"

	corecontent "github.com/Tangerg/scope/core/content"
	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

// MCP-only facts live here so Core remains the sole owner of payload and media fields.
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

func (c contentKind) admits(part corecontent.Kind) bool {
	switch c {
	case contentText:
		return part == corecontent.Text
	case contentImage, contentAudio, contentLink:
		return part == corecontent.Media
	case contentResource:
		return part == corecontent.Text || part == corecontent.Media
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
	// Inline URIs record provenance; Core Media.URI alone owns links.
	URI      string       `json:"uri,omitempty"`
	MIMEType string       `json:"mimeType,omitempty"`
	Meta     metadata.Map `json:"_meta,omitzero"`
}

func (r resourceEnvelope) contents(part corecontent.Block) (*sdkmcp.ResourceContents, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	resource := &sdkmcp.ResourceContents{URI: r.URI, Meta: metadataToMCP(r.Meta)}
	if part.Kind == corecontent.Text {
		resource.Text, resource.MIMEType = part.Text, r.MIMEType
		return resource, nil
	}
	if part.Media.Source.Kind != media.SourceBytes {
		return nil, errors.New("mcp: embedded resource requires inline content")
	}
	if r.MIMEType != "" {
		return nil, errors.New("mcp: resource metadata duplicates a Core media field")
	}
	resource.MIMEType = part.Media.MIME
	var err error
	resource.Blob, err = part.Media.Bytes()
	if err != nil {
		return nil, err
	}
	return resource, nil
}

func (r resourceEnvelope) validate() error {
	if err := (media.Source{Kind: media.SourceURI, URI: r.URI}).Validate(); err != nil {
		return fmt.Errorf("mcp: embedded resource URI: %w", err)
	}
	if r.MIMEType != "" {
		if _, err := media.ParseType(r.MIMEType); err != nil {
			return fmt.Errorf("mcp: embedded resource: %w", err)
		}
	}
	return nil
}

type coreContentEnvelope struct {
	Metadata      metadata.Map           `json:"metadata,omitzero"`
	Citations     []corecontent.Citation `json:"citations,omitempty"`
	Name          string                 `json:"name,omitempty"`
	MediaID       string                 `json:"media_id,omitempty"`
	MediaMetadata metadata.Map           `json:"media_metadata,omitempty"`
}

func (c coreContentEnvelope) isZero() bool {
	return len(c.Metadata) == 0 && len(c.Citations) == 0 && c.Name == "" && c.MediaID == "" && len(c.MediaMetadata) == 0
}

func (c coreContentEnvelope) describesMedia() bool {
	return c.Name != "" || c.MediaID != "" || len(c.MediaMetadata) != 0
}

func (c coreContentEnvelope) restore(part *corecontent.Block, kind contentKind) error {
	if _, collision := c.Metadata[ContentMetadataKey]; collision {
		return errors.New("mcp: Core content metadata contains a nested MCP envelope")
	}
	if c.describesMedia() && part.Kind != corecontent.Media {
		return errors.New("mcp: Core content metadata describes media on non-media content")
	}
	if c.Name != "" && kind == contentLink {
		return errors.New("mcp: Core content metadata duplicates the resource link name")
	}
	if part.Kind == corecontent.Media {
		if c.Name != "" {
			part.Media.Name = c.Name
		}
		part.Media.ID = c.MediaID
		if len(c.MediaMetadata) != 0 {
			part.Media.Metadata = c.MediaMetadata
		}
	}
	part.Metadata, part.Citations = c.Metadata, c.Citations
	return nil
}

func mapRemoteContent(content sdkmcp.Content) (corecontent.Block, bool, error) {
	if lo.IsNil(content) {
		return corecontent.Block{}, false, errors.New("mcp: content is nil")
	}
	if text, ok := content.(*sdkmcp.TextContent); ok && text.Text == "" {
		if len(text.Meta) != 0 || text.Annotations != nil {
			return corecontent.Block{}, false, errors.New("mcp: empty text cannot carry content metadata")
		}
		return corecontent.Block{}, false, nil
	}
	part, envelope, nativeMeta, err := splitRemoteContent(content)
	if err != nil {
		return corecontent.Block{}, false, err
	}
	if err := envelope.attach(&part, nativeMeta); err != nil {
		return corecontent.Block{}, false, err
	}
	if err := part.Validate(); err != nil {
		return corecontent.Block{}, false, fmt.Errorf("mcp: mapped content: %w", err)
	}
	return part, true, nil
}

func splitRemoteContent(content sdkmcp.Content) (corecontent.Block, contentEnvelope, sdkmcp.Meta, error) {
	switch value := content.(type) {
	case *sdkmcp.TextContent:
		part := corecontent.Block{Kind: corecontent.Text, Text: value.Text}
		return part, contentEnvelope{Kind: contentText, Annotations: value.Annotations}, value.Meta, nil
	case *sdkmcp.ImageContent:
		part, err := remoteBytesMedia(value.MIMEType, value.Data)
		return part, contentEnvelope{Kind: contentImage, Annotations: value.Annotations}, value.Meta, err
	case *sdkmcp.AudioContent:
		part, err := remoteBytesMedia(value.MIMEType, value.Data)
		return part, contentEnvelope{Kind: contentAudio, Annotations: value.Annotations}, value.Meta, err
	case *resourceLinkContent:
		return splitRemoteContent(&value.ResourceLink)
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
		return corecontent.Block{}, contentEnvelope{}, nil, fmt.Errorf("mcp: unsupported content %T", content)
	}
}

func splitRemoteResource(value *sdkmcp.EmbeddedResource) (corecontent.Block, contentEnvelope, error) {
	resource := value.Resource
	if resource == nil {
		return corecontent.Block{}, contentEnvelope{}, errors.New("mcp: embedded resource is nil")
	}
	resourceMeta, err := metadataFromMCP(resource.Meta)
	if err != nil {
		return corecontent.Block{}, contentEnvelope{}, err
	}
	envelope := contentEnvelope{Kind: contentResource, Annotations: value.Annotations,
		Resource: &resourceEnvelope{URI: resource.URI, Meta: resourceMeta}}
	var part corecontent.Block
	switch {
	case resource.Text != "" && resource.Blob != nil:
		return corecontent.Block{}, contentEnvelope{}, errors.New("mcp: embedded resource carries both text and blob")
	case resource.Text != "":
		part = corecontent.Block{Kind: corecontent.Text, Text: resource.Text}
		envelope.Resource.MIMEType = resource.MIMEType
	case len(resource.Blob) != 0:
		var resourceMedia *media.Media
		resourceMedia, err = media.NewBytes(resource.MIMEType, resource.Blob)
		part = corecontent.Block{Kind: corecontent.Media, Media: resourceMedia}
	default:
		return corecontent.Block{}, contentEnvelope{}, ErrEmptyEmbeddedResource
	}
	if err != nil {
		return corecontent.Block{}, contentEnvelope{}, err
	}
	return part, envelope, envelope.Resource.validate()
}

func (c contentEnvelope) attach(part *corecontent.Block, nativeMeta sdkmcp.Meta) error {
	var err error
	c.Meta, err = metadataFromMCP(nativeMeta)
	if err != nil {
		return err
	}
	if raw, present := c.Meta[coreContentMetadataKey]; present {
		if jsontext.Value(raw).Kind() != jsontext.KindBeginObject {
			return errors.New("mcp: Core content metadata must be an object")
		}
		var portable coreContentEnvelope
		if err := jsonv2.Unmarshal(raw, &portable, jsonv2.RejectUnknownMembers(true)); err != nil {
			return fmt.Errorf("mcp: decode Core content metadata: %w", err)
		}
		if err := portable.restore(part, c.Kind); err != nil {
			return err
		}
		delete(c.Meta, coreContentMetadataKey)
	}
	if c.Kind == contentText && len(c.Meta) == 0 && c.Annotations == nil {
		return nil
	}
	return part.Metadata.Set(ContentMetadataKey, c)
}

func metadataFromMCP(native sdkmcp.Meta) (metadata.Map, error) {
	values := metadata.Map(native).Clone()
	if err := values.Validate(); err != nil {
		return nil, fmt.Errorf("mcp: content metadata: %w", err)
	}
	return values, nil
}

func metadataToMCP(values metadata.Map) sdkmcp.Meta {
	return sdkmcp.Meta(values.Clone())
}

func remoteBytesMedia(mimeType string, data []byte) (corecontent.Block, error) {
	value, err := media.NewBytes(mimeType, data)
	if err != nil {
		return corecontent.Block{}, err
	}
	if err := value.RequireMIME(); err != nil {
		return corecontent.Block{}, err
	}
	return corecontent.Block{Kind: corecontent.Media, Media: value}, nil
}

func remoteURIMedia(mimeType, uri string) (corecontent.Block, error) {
	value, err := media.NewURI(mimeType, uri)
	if err != nil {
		return corecontent.Block{}, err
	}
	return corecontent.Block{Kind: corecontent.Media, Media: value}, nil
}

func mapServerContent(part corecontent.Block) (sdkmcp.Content, error) {
	if part.Kind == corecontent.Media && part.Media.Source.Kind == media.SourceReference {
		return nil, ErrMediaReference
	}
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
	if envelope.Kind == "" {
		if err := envelope.infer(part); err != nil {
			return nil, err
		}
	}
	if part.Kind == corecontent.Media {
		if envelope.Kind != contentLink {
			portable.Name = part.Media.Name
		}
		portable.MediaID, portable.MediaMetadata = part.Media.ID, part.Media.Metadata.Clone()
	}
	if !portable.isZero() {
		if err := envelope.Meta.Set(coreContentMetadataKey, portable); err != nil {
			return nil, err
		}
	}
	return envelope.content(part)
}

func (c contentEnvelope) content(part corecontent.Block) (sdkmcp.Content, error) {
	if err := c.validateFields(); err != nil {
		return nil, err
	}
	if !c.Kind.admits(part.Kind) {
		return nil, fmt.Errorf("mcp: content kind %q does not match Core part %q", c.Kind, part.Kind)
	}

	meta := metadataToMCP(c.Meta)
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
		link := &resourceLinkContent{ResourceLink: sdkmcp.ResourceLink{URI: uri, Name: part.Media.Name, MIMEType: part.Media.MIME,
			Title: c.Title, Description: c.Description, Size: c.Size, Icons: c.Icons, Meta: meta, Annotations: c.Annotations}}
		return link, nil
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
	if err := value.RequireMIME(); err != nil {
		return nil, err
	}
	data, err := value.Bytes()
	if err != nil {
		return nil, err
	}
	if c.Kind == contentImage {
		return &sdkmcp.ImageContent{MIMEType: value.MIME, Data: data, Meta: meta, Annotations: c.Annotations}, nil
	}
	return &sdkmcp.AudioContent{MIMEType: value.MIME, Data: data, Meta: meta, Annotations: c.Annotations}, nil
}

// MCP requires a URI even when the payload is inline bytes.
const inlineResourceURIPrefix = "scope://tool-output/"

func (c *contentEnvelope) infer(part corecontent.Block) error {
	if part.Kind == corecontent.Text {
		c.Kind = contentText
		return nil
	}
	value := part.Media
	switch value.Source.Kind {
	case media.SourceBytes:
		switch value.Type().TopLevel() {
		case media.TopLevelImage:
			c.Kind = contentImage
		case media.TopLevelAudio:
			c.Kind = contentAudio
		default:
			c.Kind = contentResource
			c.Resource = &resourceEnvelope{URI: inlineResourceURIPrefix + httpclient.PathSegment(value.Name)}
		}
	case media.SourceURI:
		c.Kind = contentLink
	default:
		return media.ErrInvalidSource
	}
	return nil
}
