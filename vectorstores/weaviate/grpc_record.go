package weaviate

import (
	"encoding/binary"
	"errors"
	"math"

	"github.com/google/uuid"
	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

// Weaviate's named SINGLE_FP32 vectors contain little-endian IEEE 754 values.
func packVector(values []float32) *pb.Vectors {
	b := make([]byte, 0, len(values)*4)
	for _, v := range values {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(v))
	}
	return &pb.Vectors{Name: vectorName, Type: pb.Vectors_VECTOR_TYPE_SINGLE_FP32, VectorBytes: b}
}

func searchRequest(collection string, request *vectorstore.Query, values []float32, where *pb.Filters, alpha *float32) *pb.SearchRequest {
	named := packVector(values)
	targets := &pb.Targets{TargetVectors: []string{vectorName}}
	near := &pb.NearVector{Targets: targets, VectorForTargets: []*pb.VectorForTarget{{Name: vectorName, Vectors: []*pb.Vectors{named}}}}
	meta := &pb.MetadataRequest{Uuid: true, Vectors: []string{vectorName}}
	out := &pb.SearchRequest{Collection: collection, Limit: uint32(request.Options.ResultLimit()), Metadata: meta, Properties: &pb.PropertiesRequest{NonRefProperties: []string{fieldContent, fieldMetadata, fieldTerms}}, Filters: where}
	if request.Options.EffectiveMode() == vectorstore.SearchModeHybrid {
		meta.Score = true
		out.HybridSearch = &pb.Hybrid{Query: request.LexicalText, Properties: []string{fieldContent}, FusionType: pb.Hybrid_FUSION_TYPE_RELATIVE_SCORE, AlphaParam: alpha, UseAlphaParam: true, Targets: targets, NearVector: near}
	} else {
		meta.Distance = true
		out.NearVector = near
	}
	return out
}

func decodeHit(hit *pb.SearchResult, mode vectorstore.SearchMode, dimensions int) (*document.Document, uuid.UUID, float64, error) {
	m := hit.GetMetadata()
	id, err := uuid.FromBytes(m.GetIdAsBytes())
	if err != nil || id == uuid.Nil {
		return nil, uuid.Nil, 0, ErrInvalidObjectID
	}
	fields := hit.GetProperties().GetNonRefProps().GetFields()
	text, okText := fields[fieldContent].GetKind().(*pb.Value_TextValue)
	facts, okFacts := fields[fieldMetadata].GetKind().(*pb.Value_TextValue)
	terms := fields[fieldTerms].GetListValue().GetTextValues()
	if len(fields) != 3 || !okText || !okFacts || terms == nil {
		return nil, id, 0, errors.New("weaviate: invalid native property shape")
	}
	doc, err := decodeDocument(id.String(), text.TextValue, facts.TextValue, terms.Values)
	if err != nil {
		return nil, id, 0, err
	}
	vectors := m.GetVectors()
	if len(vectors) != 1 || vectors[0].GetName() != vectorName || vectors[0].GetType() != pb.Vectors_VECTOR_TYPE_SINGLE_FP32 {
		return nil, id, 0, errors.New("weaviate: missing requested dense vector")
	}
	raw := vectors[0].GetVectorBytes()
	if len(raw)%4 != 0 || len(raw)/4 != dimensions {
		return nil, id, 0, errors.New("weaviate: named vector has invalid width")
	}
	for i := 0; i < len(raw); i += 4 {
		v := float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[i:])))
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, id, 0, errors.New("weaviate: non-finite named vector")
		}
	}
	if mode == vectorstore.SearchModeHybrid {
		if !m.ScorePresent {
			return nil, id, 0, errors.New("weaviate: missing hybrid score")
		}
		return doc, id, float64(m.Score), nil
	}
	if !m.DistancePresent {
		return nil, id, 0, errors.New("weaviate: missing native distance")
	}
	return doc, id, float64(m.Distance), nil
}
