// Package vectorstore instruments Core vector-store capabilities with OpenTelemetry.
package vectorstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/samber/lo"

	corevectorstore "github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/otel/errortelemetry"
	"github.com/Tangerg/scope/otel/internal/instrument"
)

const (
	instrumentationName           = "github.com/Tangerg/scope/otel/vectorstore"
	operationDurationMetric       = "db.client.operation.duration"
	operationDurationDescription  = "Vector store operation duration."
	operationDurationUnit         = "s"
	searchReturnedRowsMetric      = "db.vector.search.returned_rows"
	searchReturnedRowsDescription = "Rows returned by a vector search."
	searchReturnedRowsUnit        = "{row}"
	queryTopKKey                  = attribute.Key("db.vector.query.top_k")
	queryMinScoreKey              = attribute.Key("db.vector.query.similarity_threshold")
)

type operationName string

const (
	operationUpsert      operationName = "upsert"
	operationQuery       operationName = "query"
	operationDeleteIDs   operationName = "delete_ids"
	operationDeleteWhere operationName = "delete_where"
)

var (
	ErrInvalidConfig = errors.New("otel/vectorstore: invalid config")
)

// MiddlewareConfig identifies the database observed by vector-store
// instrumentation. System is the OpenTelemetry db.system.name value.
// Collection and Namespace identify the provider-native storage target.
type MiddlewareConfig struct {
	System         string
	Collection     string
	Namespace      string
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

func (m MiddlewareConfig) Validate() error {
	if strings.TrimSpace(m.System) == "" {
		return fmt.Errorf("%w: system is required", ErrInvalidConfig)
	}
	return nil
}

// Middleware decorates one Core operation at a time. Its returned value exposes
// only that operation; provider-specific admission methods remain on the store.
// A host using RAG must compose those methods with the decorated operations from
// that same store so admission and execution keep one policy owner.
type Middleware struct {
	system     string
	collection string
	namespace  string
	tracer     trace.Tracer
	duration   metric.Float64Histogram
	searchRows metric.Int64Histogram
}

func NewMiddleware(config MiddlewareConfig) (Middleware, error) {
	if err := config.Validate(); err != nil {
		return Middleware{}, err
	}
	system := instrument.Dimension(config.System)

	tracerProvider := instrument.TracerProvider(config.TracerProvider)
	meterProvider := instrument.MeterProvider(config.MeterProvider)
	meter := meterProvider.Meter(instrumentationName)
	duration, err := meter.Float64Histogram(
		operationDurationMetric,
		metric.WithDescription(operationDurationDescription),
		metric.WithUnit(operationDurationUnit),
	)
	if err != nil {
		return Middleware{}, fmt.Errorf("%w: create duration histogram: %w", ErrInvalidConfig, err)
	}
	searchRows, err := meter.Int64Histogram(
		searchReturnedRowsMetric,
		metric.WithDescription(searchReturnedRowsDescription),
		metric.WithUnit(searchReturnedRowsUnit),
	)
	if err != nil {
		return Middleware{}, fmt.Errorf("%w: create returned-rows histogram: %w", ErrInvalidConfig, err)
	}
	return Middleware{
		system:     system,
		collection: strings.TrimSpace(config.Collection),
		namespace:  strings.TrimSpace(config.Namespace),
		tracer:     tracerProvider.Tracer(instrumentationName),
		duration:   duration,
		searchRows: searchRows,
	}, nil
}

func (m Middleware) Upsert(next corevectorstore.Upserter) corevectorstore.Upserter {
	if lo.IsNil(next) {
		return nil
	}
	if err := m.validate(); err != nil {
		return invalidUpserter{err: err}
	}
	return upserter{middleware: m, next: next}
}

func (m Middleware) Query(next corevectorstore.Querier) corevectorstore.Querier {
	if lo.IsNil(next) {
		return nil
	}
	if err := m.validate(); err != nil {
		return invalidQuerier{err: err}
	}
	return querier{middleware: m, next: next}
}

func (m Middleware) DeleteIDs(next corevectorstore.IDDeleter) corevectorstore.IDDeleter {
	if lo.IsNil(next) {
		return nil
	}
	if err := m.validate(); err != nil {
		return invalidIDDeleter{err: err}
	}
	return idDeleter{middleware: m, next: next}
}

func (m Middleware) DeleteWhere(next corevectorstore.FilterDeleter) corevectorstore.FilterDeleter {
	if lo.IsNil(next) {
		return nil
	}
	if err := m.validate(); err != nil {
		return invalidFilterDeleter{err: err}
	}
	return filterDeleter{middleware: m, next: next}
}

func (m Middleware) validate() error {
	if lo.IsNil(m.tracer) || lo.IsNil(m.duration) || lo.IsNil(m.searchRows) {
		return fmt.Errorf("%w: middleware must be constructed with NewMiddleware", ErrInvalidConfig)
	}
	return nil
}

func (m Middleware) start(
	ctx context.Context,
	operation operationName,
	extra ...attribute.KeyValue,
) (context.Context, vectorStoreObservation) {
	startedAt := time.Now()
	metricAttributes := []attribute.KeyValue{
		semconv.DBSystemNameKey.String(m.system),
		semconv.DBOperationNameKey.String(string(operation)),
	}
	attrs := append([]attribute.KeyValue(nil), metricAttributes...)
	target := ""
	if m.collection != "" {
		collection := semconv.DBCollectionNameKey.String(m.collection)
		attrs = append(attrs, collection)
		metricAttributes = append(metricAttributes, collection)
		target = m.collection
	}
	if m.namespace != "" {
		namespace := semconv.DBNamespaceKey.String(m.namespace)
		attrs = append(attrs, namespace)
		metricAttributes = append(metricAttributes, namespace)
		if target == "" {
			target = m.namespace
		}
	}
	attrs = append(attrs, extra...)

	name := string(operation)
	if target != "" {
		name += " " + target
	}
	spanCtx, span := m.tracer.Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...),
	)
	return spanCtx, vectorStoreObservation{
		middleware:       m,
		ctx:              spanCtx,
		span:             span,
		startedAt:        startedAt,
		metricAttributes: metricAttributes,
	}
}

type vectorStoreObservation struct {
	middleware       Middleware
	ctx              context.Context
	span             trace.Span
	startedAt        time.Time
	metricAttributes []attribute.KeyValue
}

func (v vectorStoreObservation) finish(err error) {
	metricAttributes := v.metricAttributes
	if err != nil {
		errorType := errortelemetry.Classify(err)
		errortelemetry.Record(v.span, errorType)
		metricAttributes = append(metricAttributes, errorType)
	}
	v.span.End()
	v.middleware.duration.Record(
		v.ctx,
		time.Since(v.startedAt).Seconds(),
		metric.WithAttributes(metricAttributes...),
	)
}

func (v vectorStoreObservation) recordReturnedRows(count int) {
	v.middleware.searchRows.Record(
		v.ctx,
		int64(count),
		metric.WithAttributes(v.metricAttributes...),
	)
}

type upserter struct {
	middleware Middleware
	next       corevectorstore.Upserter
}

type invalidUpserter struct{ err error }

func (i invalidUpserter) Upsert(context.Context, *corevectorstore.Batch) error { return i.err }

func (u upserter) Upsert(ctx context.Context, request *corevectorstore.Batch) (err error) {
	var extra []attribute.KeyValue
	if request != nil && len(request.Records) > 1 {
		extra = append(extra, semconv.DBOperationBatchSizeKey.Int(len(request.Records)))
	}
	ctx, observation := u.middleware.start(ctx, operationUpsert, extra...)
	defer errortelemetry.Finish(&err, observation.finish)
	return u.next.Upsert(ctx, request)
}

type querier struct {
	middleware Middleware
	next       corevectorstore.Querier
}

type invalidQuerier struct{ err error }

func (i invalidQuerier) Query(context.Context, *corevectorstore.Query) (*corevectorstore.SearchResponse, error) {
	return nil, i.err
}

func (q querier) Query(ctx context.Context, request *corevectorstore.Query) (response *corevectorstore.SearchResponse, err error) {
	var topK int
	var minScore float64
	if request != nil {
		topK = request.Options.ResultLimit()
		minScore = request.Options.MinScore.Float64()
	}
	ctx, observation := q.middleware.start(ctx, operationQuery,
		queryTopKKey.Int(topK),
		queryMinScoreKey.Float64(minScore),
	)
	defer errortelemetry.Finish(&err, observation.finish)
	response, err = q.next.Query(ctx, request)
	if err == nil && response != nil {
		observation.span.SetAttributes(semconv.DBResponseReturnedRowsKey.Int(len(response.Results)))
		observation.recordReturnedRows(len(response.Results))
	}
	return response, err
}

type idDeleter struct {
	middleware Middleware
	next       corevectorstore.IDDeleter
}

type invalidIDDeleter struct{ err error }

func (i invalidIDDeleter) DeleteIDs(context.Context, []string) error { return i.err }

func (i idDeleter) DeleteIDs(ctx context.Context, ids []string) (err error) {
	var extra []attribute.KeyValue
	if len(ids) > 1 {
		extra = append(extra, semconv.DBOperationBatchSizeKey.Int(len(ids)))
	}
	ctx, observation := i.middleware.start(ctx, operationDeleteIDs, extra...)
	defer errortelemetry.Finish(&err, observation.finish)
	return i.next.DeleteIDs(ctx, ids)
}

type filterDeleter struct {
	middleware Middleware
	next       corevectorstore.FilterDeleter
}

type invalidFilterDeleter struct{ err error }

func (i invalidFilterDeleter) DeleteWhere(context.Context, filter.Predicate) error { return i.err }

func (f filterDeleter) DeleteWhere(ctx context.Context, predicate filter.Predicate) (err error) {
	ctx, observation := f.middleware.start(ctx, operationDeleteWhere)
	defer errortelemetry.Finish(&err, observation.finish)
	return f.next.DeleteWhere(ctx, predicate)
}
