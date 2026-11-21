package instrument

import (
	"context"
	"fmt"
	"time"

	"github.com/samber/lo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/semconv/v1.41.0/genaiconv"

	"github.com/Tangerg/scope/otel/errortelemetry"
	"github.com/Tangerg/scope/otel/internal/genaimetric"
)

// Operation is one GenAI client call. It owns the semantic-convention rules
// every capability records alike, so they change in one place: the model
// dimensions of each metric, the streaming chunk timings, and how a failure is
// reported. It is not safe for concurrent use.
type Operation struct {
	client        Client
	name          genaiconv.OperationNameAttr
	measurement   Measurement
	lastChunkAt   time.Time
	requestModel  string
	responseModel string
}

func (o *Operation) SetAttributes(attributes ...attribute.KeyValue) {
	o.measurement.SetAttributes(attributes...)
}

// ObserveResponseModel records the model that served the call on the span and
// as a dimension of every later metric; an empty model is unknown.
func (o *Operation) ObserveResponseModel(model string) {
	if model == "" {
		return
	}
	o.responseModel = model
	o.measurement.SetAttributes(semconv.GenAIResponseModel(model))
}

func (o *Operation) RecordTokens(ctx context.Context, usage genaiconv.ClientTokenUsage, tokenType genaiconv.TokenTypeAttr, count int64) {
	usage.Record(ctx, count, o.name, genaiconv.ProviderNameAttr(o.client.Provider), tokenType, o.modelAttributes()...)
}

// ObserveChunk records the time to the first chunk on the span and in timing,
// then the interval since the previous chunk for every later one.
func (o *Operation) ObserveChunk(ctx context.Context, timing ChunkTiming, receivedAt time.Time) {
	provider := genaiconv.ProviderNameAttr(o.client.Provider)
	if o.lastChunkAt.IsZero() {
		elapsed := receivedAt.Sub(o.measurement.startedAt).Seconds()
		o.measurement.SetAttributes(semconv.GenAIResponseTimeToFirstChunk(elapsed))
		timing.firstChunk.Record(ctx, elapsed, o.name, provider, o.modelAttributes()...)
	} else {
		timing.chunkInterval.Record(ctx, receivedAt.Sub(o.lastChunkAt).Seconds(), o.name, provider, o.modelAttributes()...)
	}
	o.lastChunkAt = receivedAt
}

// End reports a failure as the measurement does and as a GenAI exception
// event at the instant the operation ended.
func (o *Operation) End(ctx context.Context, err error) {
	errorType := o.client.classify(err)
	attributes := append(o.modelAttributes(),
		semconv.GenAIOperationNameKey.String(string(o.name)),
		semconv.GenAIProviderNameKey.String(o.client.Provider),
	)
	finishedAt := o.measurement.End(ctx, o.client.Duration.Inst(), errorType, attributes...)
	if errorType.Valid() {
		errortelemetry.EmitGenAIException(ctx, o.client.Logger, errorType, finishedAt)
	}
}

func (o *Operation) modelAttributes() []attribute.KeyValue {
	var attributes []attribute.KeyValue
	if o.requestModel != "" {
		attributes = append(attributes, semconv.GenAIRequestModel(o.requestModel))
	}
	if o.responseModel != "" {
		attributes = append(attributes, semconv.GenAIResponseModel(o.responseModel))
	}
	return attributes
}

// ChunkTiming holds the histograms of a capability that streams its output.
type ChunkTiming struct {
	firstChunk    genaiconv.ClientOperationTimeToFirstChunk
	chunkInterval genaiconv.ClientOperationTimePerOutputChunk
}

func NewChunkTiming(meter metric.Meter) (ChunkTiming, error) {
	firstChunk, err := genaiconv.NewClientOperationTimeToFirstChunk(meter, genaimetric.DurationBuckets())
	if err != nil {
		return ChunkTiming{}, fmt.Errorf("create first-chunk histogram: %w", err)
	}
	chunkInterval, err := genaiconv.NewClientOperationTimePerOutputChunk(meter, genaimetric.DurationBuckets())
	if err != nil {
		return ChunkTiming{}, fmt.Errorf("create chunk-interval histogram: %w", err)
	}
	return ChunkTiming{firstChunk: firstChunk, chunkInterval: chunkInterval}, nil
}

func (c ChunkTiming) Ready() bool {
	return !lo.IsNil(c.firstChunk.Inst()) && !lo.IsNil(c.chunkInterval.Inst())
}
