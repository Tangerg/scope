package openai

import (
	"mime"
	"strings"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"

	"github.com/Tangerg/scope/core/media"
)

type responsesMediaKind uint8

const (
	responsesMediaImage responsesMediaKind = iota
	responsesMediaFile
)

type responsesMedia struct {
	kind     responsesMediaKind
	name     string
	fileID   string
	fileURL  string
	fileData string
	imageURL string
}

func projectResponsesMedia(value *media.Media) (responsesMedia, error) {
	mediaType, _, err := mime.ParseMediaType(value.MIME)
	if err != nil {
		return responsesMedia{}, err
	}
	if strings.HasPrefix(mediaType, "image/") {
		return projectResponsesImage(value)
	}
	return projectResponsesFile(value)
}

func projectResponsesImage(value *media.Media) (responsesMedia, error) {
	projected := responsesMedia{kind: responsesMediaImage}
	if value.Source.Kind == media.SourceReference {
		reference, err := value.Reference()
		if err != nil {
			return responsesMedia{}, err
		}
		projected.fileID = reference
		return projected, nil
	}
	location, err := mediaLocation(value)
	if err != nil {
		return responsesMedia{}, err
	}
	projected.imageURL = location
	return projected, nil
}

func projectResponsesFile(value *media.Media) (responsesMedia, error) {
	projected := responsesMedia{kind: responsesMediaFile, name: value.Name}
	switch value.Source.Kind {
	case media.SourceReference:
		reference, err := value.Reference()
		if err != nil {
			return responsesMedia{}, err
		}
		projected.fileID = reference
	case media.SourceURI:
		uri, err := value.URI()
		if err != nil {
			return responsesMedia{}, err
		}
		projected.fileURL = uri
	case media.SourceBytes:
		location, err := mediaLocation(value)
		if err != nil {
			return responsesMedia{}, err
		}
		projected.fileData = location
	default:
		return responsesMedia{}, media.ErrInvalidSource
	}
	return projected, nil
}

func mapResponsesMedia(value *media.Media) (responses.ResponseInputContentUnionParam, error) {
	projected, err := projectResponsesMedia(value)
	if err != nil {
		return responses.ResponseInputContentUnionParam{}, err
	}
	if projected.kind == responsesMediaImage {
		image := &responses.ResponseInputImageParam{
			Detail:   responses.ResponseInputImageDetailAuto,
			FileID:   optionalString(projected.fileID),
			ImageURL: optionalString(projected.imageURL),
		}
		return responses.ResponseInputContentUnionParam{OfInputImage: image}, nil
	}
	file := &responses.ResponseInputFileParam{
		Filename: openaisdk.String(projected.name),
		FileID:   optionalString(projected.fileID),
		FileURL:  optionalString(projected.fileURL),
		FileData: optionalString(projected.fileData),
	}
	return responses.ResponseInputContentUnionParam{OfInputFile: file}, nil
}

func mapResponsesToolMedia(value *media.Media) (responses.ResponseFunctionCallOutputItemUnionParam, error) {
	projected, err := projectResponsesMedia(value)
	if err != nil {
		return responses.ResponseFunctionCallOutputItemUnionParam{}, err
	}
	if projected.kind == responsesMediaImage {
		image := &responses.ResponseInputImageContentParam{
			Detail:   responses.ResponseInputImageContentDetailAuto,
			FileID:   optionalString(projected.fileID),
			ImageURL: optionalString(projected.imageURL),
		}
		return responses.ResponseFunctionCallOutputItemUnionParam{OfInputImage: image}, nil
	}
	file := &responses.ResponseInputFileContentParam{
		Filename: openaisdk.String(projected.name),
		FileID:   optionalString(projected.fileID),
		FileURL:  optionalString(projected.fileURL),
		FileData: optionalString(projected.fileData),
	}
	return responses.ResponseFunctionCallOutputItemUnionParam{OfInputFile: file}, nil
}

func optionalString(value string) param.Opt[string] {
	if value == "" {
		return param.Opt[string]{}
	}
	return openaisdk.String(value)
}
