package pinecone

import (
	"fmt"

	pineconesdk "github.com/pinecone-io/go-pinecone/v7/pinecone"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	MaxVectorsPerUpsert = 1000
	// Decimal bytes keep the adapter within Pinecone's published 2 MB limit.
	upsertRequestByteBudget = 2_000_000

	upsertVectorsFieldNumber   protowire.Number = 1
	upsertNamespaceFieldNumber protowire.Number = 2
	vectorIDFieldNumber        protowire.Number = 1
	vectorValuesFieldNumber    protowire.Number = 2
	vectorMetadataFieldNumber  protowire.Number = 3
)

// Planning finishes before the first RPC, so an oversized later record cannot
// leave an earlier group committed. Every group retains input order for write
// acknowledgment classification.
type upsertBatchPlan [][]*pineconesdk.Vector

func newUpsertBatchPlan(namespace string, points []*pineconesdk.Vector) (upsertBatchPlan, error) {
	var envelopeBytes int
	if namespace != "" {
		envelopeBytes = protowire.SizeTag(upsertNamespaceFieldNumber) + protowire.SizeBytes(len(namespace))
	}
	var plan upsertBatchPlan
	start, requestBytes := 0, envelopeBytes
	for index, point := range points {
		vectorBytes, err := denseVectorWireSize(point)
		if err != nil {
			return nil, err
		}
		recordBytes := protowire.SizeTag(upsertVectorsFieldNumber) + protowire.SizeBytes(vectorBytes)
		if envelopeBytes+recordBytes > upsertRequestByteBudget {
			return nil, fmt.Errorf("%w: pinecone document %q exceeds the %d-byte upsert request budget", vectorstore.ErrInvalidDocument, point.Id, upsertRequestByteBudget)
		}
		if index-start == MaxVectorsPerUpsert || requestBytes+recordBytes > upsertRequestByteBudget {
			plan = append(plan, points[start:index])
			start, requestBytes = index, envelopeBytes
		}
		requestBytes += recordBytes
	}
	if start < len(points) {
		plan = append(plan, points[start:])
	}
	return plan, nil
}

func (u upsertBatchPlan) ids() []string {
	var ids []string
	for _, group := range u {
		for _, point := range group {
			ids = append(ids, point.Id)
		}
	}
	return ids
}

// The SDK hides its protobuf request types. These sizes include every field
// its dense-vector conversion emits, including packed values and envelopes;
// SDK request tests compare them with the actual protobuf message size.
func denseVectorWireSize(point *pineconesdk.Vector) (int, error) {
	if point == nil || point.SparseValues != nil {
		return 0, fmt.Errorf("%w: pinecone upsert requires a dense vector", vectorstore.ErrInvalidRequest)
	}
	var size int
	if point.Id != "" {
		size += protowire.SizeTag(vectorIDFieldNumber) + protowire.SizeBytes(len(point.Id))
	}
	if point.Values != nil && len(*point.Values) != 0 {
		size += protowire.SizeTag(vectorValuesFieldNumber) + protowire.SizeBytes(len(*point.Values)*protowire.SizeFixed32())
	}
	if point.Metadata != nil {
		size += protowire.SizeTag(vectorMetadataFieldNumber) + protowire.SizeBytes(proto.Size(point.Metadata))
	}
	return size, nil
}
