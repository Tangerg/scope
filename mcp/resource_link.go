package mcp

import (
	jsonv2 "encoding/json/v2"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"
)

// The SDK's shared content wire shape omits an empty name. Resource links
// require that field even when unnamed, so this variant owns its entire wire
// shape while the embedded SDK value supplies its sealed Content contract.
type resourceLinkContent struct {
	sdkmcp.ResourceLink
}

func (r *resourceLinkContent) MarshalJSON() ([]byte, error) {
	return jsonv2.Marshal(struct {
		Type        contentKind         `json:"type"`
		URI         string              `json:"uri"`
		Name        string              `json:"name"`
		Title       string              `json:"title,omitempty"`
		Description string              `json:"description,omitempty"`
		MIMEType    string              `json:"mimeType,omitempty"`
		Size        *int64              `json:"size,omitzero"`
		Meta        sdkmcp.Meta         `json:"_meta,omitempty"`
		Annotations *sdkmcp.Annotations `json:"annotations,omitzero"`
		Icons       []sdkmcp.Icon       `json:"icons,omitempty"`
	}{
		Type: contentLink, URI: r.URI, Name: r.Name, Title: r.Title,
		Description: r.Description, MIMEType: r.MIMEType, Size: r.Size,
		Meta: r.Meta, Annotations: r.Annotations, Icons: r.Icons,
	})
}
