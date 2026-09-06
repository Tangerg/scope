// Package vectorstore instruments Core vector-store capabilities with OpenTelemetry.
package vectorstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apiotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/samber/lo"

	corevectorstore "github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/otel/internal/errortelemetry"
)

const (
	instrumentationName           = "github.com/Tangerg/scope/otel/vectorstore"
	operationDurationMetric       = "db.client.operation.duration"
	operationDurationDescription  = "Vector store operation duration."
	operationDurationUnit         = "s"
	searchReturnedRowsMetric      = "db.vector.search.returned_rows"
	searchReturnedRowsDescription = "Rows returned by a vector search."
	searchReturnedRowsUnit        = "{row}"
	errorTypeCanceled             = "context.canceled"
	errorTypeDeadlineExceeded     = "context.deadline_exceeded"
)

type operationName string

const (
	operationIndex       operationName = "index"
	operationSearch      operationName = "search"
	operationDeleteIDs   operationName = "delete_ids"
	operationDeleteWhere operationName = "delete_where"
	queryTopKKey                       = attribute.Key("db.vector.query.top_k")
	queryMinScoreKey                   = attribute.Key("db.vector.query.similarity_threshold")
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

// Middleware instruments vector-store capabilities without changing
// the capability set of the wrapped value.
type Middleware struct {
	system     string
	collection string
	namespace  string
	tracer     trace.Tracer
	duration   metric.Float64Histogram
	searchRows metric.Int64Histogram
}

// NewMiddleware fixes instrument identity and provider binding once so index
// and search operations contribute to stable telemetry series.
func NewMiddleware(config MiddlewareConfig) (Middleware, error) {
	if err := config.Validate(); err != nil {
		return Middleware{}, err
	}
	system := strings.ToLower(strings.TrimSpace(config.System))

	tracerProvider := config.TracerProvider
	if lo.IsNil(tracerProvider) {
		tracerProvider = apiotel.GetTracerProvider()
	}
	meterProvider := config.MeterProvider
	if lo.IsNil(meterProvider) {
		meterProvider = apiotel.GetMeterProvider()
	}
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

// Index instruments only the [corevectorstore.Indexer] capability.
func (m Middleware) Index(next corevectorstore.Indexer) corevectorstore.Indexer {
	if lo.IsNil(next) {
		return nil
	}
	if err := m.validate(); err != nil {
		return invalidIndexer{err: err}
	}
	return indexer{middleware: m, next: next}
}

// Search instruments only the [corevectorstore.Searcher] capability.
func (m Middleware) Search(next corevectorstore.Searcher) corevectorstore.Searcher {
	if lo.IsNil(next) {
		return nil
	}
	if err := m.validate(); err != nil {
		return invalidSearcher{err: err}
	}
	return searcher{middleware: m, next: next}
}

// DeleteIDs instruments only the [corevectorstore.IDDeleter] capability.
func (m Middleware) DeleteIDs(next corevectorstore.IDDeleter) corevectorstore.IDDeleter {
	if lo.IsNil(next) {
		return nil
	}
	if err := m.validate(); err != nil {
		return invalidIDDeleter{err: err}
	}
	return idDeleter{middleware: m, next: next}
}

// DeleteWhere instruments only the [corevectorstore.FilterDeleter] capability.
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
		errorType := vectorStoreErrorType(err)
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

func vectorStoreErrorType(err error) attribute.KeyValue {
	switch {
	case errors.Is(err, context.Canceled):
		return semconv.ErrorTypeKey.String(errorTypeCanceled)
	case errors.Is(err, context.DeadlineExceeded):
		return semconv.ErrorTypeKey.String(errorTypeDeadlineExceeded)
	default:
		return semconv.ErrorType(err)
	}
}

type indexer struct {
	middleware Middleware
	next       corevectorstore.Indexer
}

type invalidIndexer struct{ err error }

func (i invalidIndexer) Index(context.Context, *corevectorstore.IndexRequest) error { return i.err }

func (i indexer) Index(ctx context.Context, request *corevectorstore.IndexRequest) error {
	var extra []attribute.KeyValue
	if request != nil && len(request.Documents) > 1 {
		extra = append(extra, semconv.DBOperationBatchSizeKey.Int(len(request.Documents)))
	}
	ctx, observation := i.middleware.start(ctx, operationIndex, extra...)
	err := i.next.Index(ctx, request)
	observation.finish(err)
	return err
}

type searcher struct {
	middleware Middleware
	next       corevectorstore.Searcher
}

type invalidSearcher struct{ err error }

func (i invalidSearcher) Search(context.Context, *corevectorstore.SearchRequest) (*corevectorstore.SearchResponse, error) {
	return nil, i.err
}

func (s searcher) Search(ctx context.Context, request *corevectorstore.SearchRequest) (*corevectorstore.SearchResponse, error) {
	var topK int
	var minScore float64
	if request != nil {
		topK = request.Options.ResultLimit()
		minScore = request.Options.MinScore.Float64()
	}
	ctx, observation := s.middleware.start(ctx, operationSearch,
		queryTopKKey.Int(topK),
		queryMinScoreKey.Float64(minScore),
	)
	response, err := s.next.Search(ctx, request)
	if err == nil && response != nil {
		observation.span.SetAttributes(semconv.DBResponseReturnedRowsKey.Int(len(response.Results)))
		observation.recordReturnedRows(len(response.Results))
	}
	observation.finish(err)
	return response, err
}

type idDeleter struct {
	middleware Middleware
	next       corevectorstore.IDDeleter
}

type invalidIDDeleter struct{ err error }

func (i invalidIDDeleter) DeleteIDs(context.Context, []string) error { return i.err }

func (i idDeleter) DeleteIDs(ctx context.Context, ids []string) error {
	var extra []attribute.KeyValue
	if len(ids) > 1 {
		extra = append(extra, semconv.DBOperationBatchSizeKey.Int(len(ids)))
	}
	ctx, observation := i.middleware.start(ctx, operationDeleteIDs, extra...)
	err := i.next.DeleteIDs(ctx, ids)
	observation.finish(err)
	return err
}

type filterDeleter struct {
	middleware Middleware
	next       corevectorstore.FilterDeleter
}

type invalidFilterDeleter struct{ err error }

func (i invalidFilterDeleter) DeleteWhere(context.Context, filter.Predicate) error { return i.err }

func (f filterDeleter) DeleteWhere(ctx context.Context, predicate filter.Predicate) error {
	ctx, observation := f.middleware.start(ctx, operationDeleteWhere)
	err := f.next.DeleteWhere(ctx, predicate)
	observation.finish(err)
	return err
}
