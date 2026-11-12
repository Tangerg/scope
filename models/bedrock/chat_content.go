package bedrock

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/Tangerg/scope/core/media"
)

const (
	mediaSubtypeJPG       = "jpg"
	mediaSubtypeXM4A      = "x-m4a"
	mediaSubtypeQuickTime = "quicktime"
	mediaSubtype3GPP      = "3gpp"
	documentMIMEPDF       = "application/pdf"
	documentMIMECSV       = "text/csv"
	documentMIMEWord      = "application/msword"
	documentMIMEWordXML   = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	documentMIMEExcel     = "application/vnd.ms-excel"
	documentMIMEExcelXML  = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	documentMIMEHTML      = "text/html"
	documentMIMEText      = "text/plain"
	documentMIMEMarkdown  = "text/markdown"
)

var bedrockDocumentNamePattern = regexp.MustCompile(`^[[:alnum:]()\[\]-]+(?: [[:alnum:]()\[\]-]+)*$`)

func mediaToBlock(value *media.Media) (types.ContentBlock, error) {
	if value == nil {
		return nil, errors.New("nil media")
	}
	if err := value.RequireMIME(); err != nil {
		return nil, err
	}
	mediaType := value.Type()
	switch mediaType.TopLevel() {
	case media.TopLevelImage:
		format, ok := bedrockImageFormat(mediaType.Subtype())
		if !ok {
			return nil, fmt.Errorf("unsupported Bedrock image MIME %q", value.MIME)
		}
		source, err := bedrockImageSource(value)
		if err != nil {
			return nil, err
		}
		return &types.ContentBlockMemberImage{Value: types.ImageBlock{Format: format, Source: source}}, nil
	case media.TopLevelAudio:
		format, ok := bedrockAudioFormat(mediaType.Subtype())
		if !ok {
			return nil, fmt.Errorf("unsupported Bedrock audio MIME %q", value.MIME)
		}
		source, err := bedrockAudioSource(value)
		if err != nil {
			return nil, err
		}
		return &types.ContentBlockMemberAudio{Value: types.AudioBlock{Format: format, Source: source}}, nil
	case media.TopLevelVideo:
		format, ok := bedrockVideoFormat(mediaType.Subtype())
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
	if subtype == mediaSubtypeJPG {
		return types.ImageFormatJpeg, true
	}
	format := types.ImageFormat(subtype)
	return format, slices.Contains(format.Values(), format)
}

func bedrockAudioFormat(subtype string) (types.AudioFormat, bool) {
	if subtype == mediaSubtypeXM4A {
		return types.AudioFormatM4a, true
	}
	format := types.AudioFormat(subtype)
	return format, slices.Contains(format.Values(), format)
}

func bedrockVideoFormat(subtype string) (types.VideoFormat, bool) {
	switch subtype {
	case mediaSubtypeQuickTime:
		return types.VideoFormatMov, true
	case mediaSubtype3GPP:
		return types.VideoFormatThreeGp, true
	default:
		format := types.VideoFormat(subtype)
		return format, slices.Contains(format.Values(), format)
	}
}

func bedrockDocumentFormat(mediaType media.Type) (types.DocumentFormat, bool) {
	switch mediaType {
	case documentMIMEPDF:
		return types.DocumentFormatPdf, true
	case documentMIMECSV:
		return types.DocumentFormatCsv, true
	case documentMIMEWord:
		return types.DocumentFormatDoc, true
	case documentMIMEWordXML:
		return types.DocumentFormatDocx, true
	case documentMIMEExcel:
		return types.DocumentFormatXls, true
	case documentMIMEExcelXML:
		return types.DocumentFormatXlsx, true
	case documentMIMEHTML:
		return types.DocumentFormatHtml, true
	case documentMIMEText:
		return types.DocumentFormatTxt, true
	case documentMIMEMarkdown:
		return types.DocumentFormatMd, true
	default:
		return "", false
	}
}
