package bedrock

import (
	"errors"
	"fmt"
	"mime"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/Tangerg/scope/core/media"
)

var bedrockDocumentNamePattern = regexp.MustCompile(`^[[:alnum:]()\[\]-]+(?: [[:alnum:]()\[\]-]+)*$`)

func toBedrockDocument(value any) document.Interface {
	if value == nil {
		return nil
	}
	if object, ok := value.(map[string]any); ok && len(object) == 0 {
		return nil
	}
	return document.NewLazyDocument(value)
}

func mediaToBlock(value *media.Media) (types.ContentBlock, error) {
	if value == nil {
		return nil, errors.New("nil media")
	}
	mediaType, _, err := mime.ParseMediaType(value.MIME)
	if err != nil {
		return nil, fmt.Errorf("invalid media MIME %q: %w", value.MIME, err)
	}
	major, subtype, ok := strings.Cut(strings.ToLower(mediaType), "/")
	if !ok {
		return nil, fmt.Errorf("invalid media MIME %q", value.MIME)
	}

	switch major {
	case "image":
		format, ok := bedrockImageFormat(subtype)
		if !ok {
			return nil, fmt.Errorf("unsupported Bedrock image MIME %q", value.MIME)
		}
		source, err := bedrockImageSource(value)
		if err != nil {
			return nil, err
		}
		return &types.ContentBlockMemberImage{Value: types.ImageBlock{Format: format, Source: source}}, nil
	case "audio":
		format, ok := bedrockAudioFormat(subtype)
		if !ok {
			return nil, fmt.Errorf("unsupported Bedrock audio MIME %q", value.MIME)
		}
		source, err := bedrockAudioSource(value)
		if err != nil {
			return nil, err
		}
		return &types.ContentBlockMemberAudio{Value: types.AudioBlock{Format: format, Source: source}}, nil
	case "video":
		format, ok := bedrockVideoFormat(subtype)
		if !ok {
			return nil, fmt.Errorf("unsupported Bedrock video MIME %q", value.MIME)
		}
		source, err := bedrockVideoSource(value)
		if err != nil {
			return nil, err
		}
		return &types.ContentBlockMemberVideo{Value: types.VideoBlock{Format: format, Source: source}}, nil
	default:
		format, ok := bedrockDocumentFormat(mediaType)
		if !ok {
			return nil, fmt.Errorf("unsupported Bedrock document MIME %q", value.MIME)
		}
		source, err := bedrockDocumentSource(value)
		if err != nil {
			return nil, err
		}
		name, err := bedrockDocumentName(value.Name)
		if err != nil {
			return nil, err
		}
		return &types.ContentBlockMemberDocument{Value: types.DocumentBlock{
			Format: format,
			Name:   aws.String(name),
			Source: source,
		}}, nil
	}
}

func bedrockImageSource(value *media.Media) (types.ImageSource, error) {
	switch value.Source.Kind {
	case media.SourceBytes:
		data, err := value.Bytes()
		if err != nil {
			return nil, err
		}
		return &types.ImageSourceMemberBytes{Value: data}, nil
	default:
		location, err := bedrockS3Location(value)
		if err != nil {
			return nil, err
		}
		return &types.ImageSourceMemberS3Location{Value: location}, nil
	}
}

func bedrockAudioSource(value *media.Media) (types.AudioSource, error) {
	switch value.Source.Kind {
	case media.SourceBytes:
		data, err := value.Bytes()
		if err != nil {
			return nil, err
		}
		return &types.AudioSourceMemberBytes{Value: data}, nil
	default:
		location, err := bedrockS3Location(value)
		if err != nil {
			return nil, err
		}
		return &types.AudioSourceMemberS3Location{Value: location}, nil
	}
}

func bedrockVideoSource(value *media.Media) (types.VideoSource, error) {
	switch value.Source.Kind {
	case media.SourceBytes:
		data, err := value.Bytes()
		if err != nil {
			return nil, err
		}
		return &types.VideoSourceMemberBytes{Value: data}, nil
	default:
		location, err := bedrockS3Location(value)
		if err != nil {
			return nil, err
		}
		return &types.VideoSourceMemberS3Location{Value: location}, nil
	}
}

func bedrockDocumentSource(value *media.Media) (types.DocumentSource, error) {
	switch value.Source.Kind {
	case media.SourceBytes:
		data, err := value.Bytes()
		if err != nil {
			return nil, err
		}
		return &types.DocumentSourceMemberBytes{Value: data}, nil
	default:
		location, err := bedrockS3Location(value)
		if err != nil {
			return nil, err
		}
		return &types.DocumentSourceMemberS3Location{Value: location}, nil
	}
}

func bedrockS3Location(value *media.Media) (types.S3Location, error) {
	var location string
	var err error
	switch value.Source.Kind {
	case media.SourceURI:
		location, err = value.URI()
	case media.SourceReference:
		location, err = value.Reference()
	default:
		return types.S3Location{}, fmt.Errorf("bedrock media source %q is unsupported; use bytes or an s3:// URI", value.Source.Kind)
	}
	if err != nil {
		return types.S3Location{}, err
	}
	if !strings.HasPrefix(location, "s3://") || len(strings.TrimPrefix(location, "s3://")) == 0 {
		return types.S3Location{}, fmt.Errorf("bedrock media URI %q must use s3://", location)
	}
	return types.S3Location{Uri: aws.String(location)}, nil
}

func bedrockDocumentName(name string) (string, error) {
	if name == "" {
		return "document", nil
	}
	name = strings.TrimSuffix(name, path.Ext(name))
	if !bedrockDocumentNamePattern.MatchString(name) {
		return "", fmt.Errorf("bedrock document name %q may contain only alphanumerics, single spaces, hyphens, parentheses, and square brackets", name)
	}
	return name, nil
}

func bedrockImageFormat(subtype string) (types.ImageFormat, bool) {
	switch subtype {
	case "png":
		return types.ImageFormatPng, true
	case "jpeg", "jpg":
		return types.ImageFormatJpeg, true
	case "gif":
		return types.ImageFormatGif, true
	case "webp":
		return types.ImageFormatWebp, true
	default:
		return "", false
	}
}

func bedrockAudioFormat(subtype string) (types.AudioFormat, bool) {
	aliases := map[string]types.AudioFormat{
		"mpeg":  types.AudioFormatMpeg,
		"mp3":   types.AudioFormatMp3,
		"x-m4a": types.AudioFormatM4a,
	}
	if value, ok := aliases[subtype]; ok {
		return value, true
	}
	format := types.AudioFormat(subtype)
	return format, slices.Contains(format.Values(), format)
}

func bedrockVideoFormat(subtype string) (types.VideoFormat, bool) {
	aliases := map[string]types.VideoFormat{"quicktime": types.VideoFormatMov, "3gpp": types.VideoFormatThreeGp}
	if value, ok := aliases[subtype]; ok {
		return value, true
	}
	format := types.VideoFormat(subtype)
	return format, slices.Contains(format.Values(), format)
}

func bedrockDocumentFormat(mediaType string) (types.DocumentFormat, bool) {
	formats := map[string]types.DocumentFormat{
		"application/pdf":    types.DocumentFormatPdf,
		"text/csv":           types.DocumentFormatCsv,
		"application/msword": types.DocumentFormatDoc,
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document": types.DocumentFormatDocx,
		"application/vnd.ms-excel": types.DocumentFormatXls,
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": types.DocumentFormatXlsx,
		"text/html":     types.DocumentFormatHtml,
		"text/plain":    types.DocumentFormatTxt,
		"text/markdown": types.DocumentFormatMd,
	}
	format, ok := formats[mediaType]
	return format, ok
}
