// Package history instruments conversation-history capabilities with OpenTelemetry.
package history

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

	"github.com/Tangerg/scope/core/chat"
	corehistory "github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/otel/internal/errortelemetry"
)

const (
	instrumentationName          = "github.com/Tangerg/scope/otel/history"
	operationAttributeName       = "chat_history.operation.name"
	messageCountAttribute        = "chat_history.message.count"
	conversationCountAttribute   = "chat_history.conversation.count"
	operationDurationMetric      = "chat_history.operation.duration"
	operationDurationUnit        = "s"
	operationDurationDescription = "Conversation history operation duration."
	errorTypeCanceled            = "context.canceled"
	errorTypeDeadlineExceeded    = "context.deadline_exceeded"
)

type historyOperation string

const (
	operationRead  historyOperation = "read"
	operationWrite historyOperation = "write"
	operationClear historyOperation = "clear"
	operationList  historyOperation = "list"
)

func (h historyOperation) spanName() string { return "history." + string(h) }

var (
	ErrInvalidConfig = errors.New("otel/history: invalid config")
)

// MiddlewareConfig identifies the storage system observed by history
// instrumentation.
type MiddlewareConfig struct {
	System         string
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

func (m MiddlewareConfig) Validate() error {
	if strings.TrimSpace(m.System) == "" {
		return fmt.Errorf("%w: system is required", ErrInvalidConfig)
	}
	return nil
}

// Middleware instruments history capabilities without entering a
// provider package or changing the wrapped operation's result.
type Middleware struct {
	system   string
	tracer   trace.Tracer
	duration metric.Float64Histogram
}

// NewMiddleware fixes instrument identity and provider binding once so every
// history operation contributes to the same telemetry series.
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
	duration, err := meterProvider.Meter(instrumentationName).Float64Histogram(
		operationDurationMetric,
		metric.WithDescription(operationDurationDescription),
		metric.WithUnit(operationDurationUnit),
	)
	if err != nil {
		return Middleware{}, fmt.Errorf("%w: create duration histogram: %w", ErrInvalidConfig, err)
	}
	return Middleware{
		system: system,
		tracer: tracerProvider.Tracer(
			instrumentationName,
			trace.WithSchemaURL(semconv.SchemaURL),
		),
		duration: duration,
	}, nil
}

// Store instruments the ordinary read, write, and clear capabilities.
func (m Middleware) Store(next corehistory.Store) corehistory.Store {
	if lo.IsNil(next) {
		return nil
	}
	if err := m.validate(); err != nil {
		return invalidHistoryStore{err: err}
	}
	return historyStore{middleware: m, next: next}
}

// Conversations instruments the optional cross-conversation listing
// capability without synthesizing it for stores that do not provide it.
func (m Middleware) Conversations(next corehistory.Lister) corehistory.Lister {
	if lo.IsNil(next) {
		return nil
	}
	if err := m.validate(); err != nil {
		return invalidHistoryLister{err: err}
	}
	return historyLister{middleware: m, next: next}
}

func (m Middleware) validate() error {
	if lo.IsNil(m.tracer) || lo.IsNil(m.duration) {
		return fmt.Errorf("%w: middleware must be constructed with NewMiddleware", ErrInvalidConfig)
	}
	return nil
}

func (m Middleware) start(
	ctx context.Context,
	operation historyOperation,
	conversationID corehistory.ConversationID,
	extra ...attribute.KeyValue,
) (context.Context, historyObservation) {
	startedAt := time.Now()
	attrs := make([]attribute.KeyValue, 0, 3+len(extra))
	metricAttributes := []attribute.KeyValue{
		semconv.DBSystemNameKey.String(m.system),
		attribute.String(operationAttributeName, string(operation)),
	}
	attrs = append(attrs, metricAttributes...)
	if conversationID != "" {
		attrs = append(attrs, semconv.GenAIConversationID(conversationID.String()))
	}
	attrs = append(attrs, extra...)
	spanCtx, span := m.tracer.Start(ctx, operation.spanName(),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...),
	)
	return spanCtx, historyObservation{
		middleware:       m,
		ctx:              spanCtx,
		span:             span,
		startedAt:        startedAt,
		metricAttributes: metricAttributes,
	}
}

type historyObservation struct {
	middleware       Middleware
	ctx              context.Context
	span             trace.Span
	startedAt        time.Time
	metricAttributes []attribute.KeyValue
}

func (h historyObservation) finish(err error) {
	metricAttributes := h.metricAttributes
	if err != nil {
		errorType := historyErrorType(err)
		errortelemetry.Record(h.span, errorType)
		metricAttributes = append(metricAttributes, errorType)
	}
	h.span.End()
	h.middleware.duration.Record(
		h.ctx,
		time.Since(h.startedAt).Seconds(),
		metric.WithAttributes(metricAttributes...),
	)
}

func historyErrorType(err error) attribute.KeyValue {
	switch {
	case errors.Is(err, context.Canceled):
		return semconv.ErrorTypeKey.String(errorTypeCanceled)
	case errors.Is(err, context.DeadlineExceeded):
		return semconv.ErrorTypeKey.String(errorTypeDeadlineExceeded)
	default:
		return semconv.ErrorType(err)
	}
}

type historyStore struct {
	middleware Middleware
	next       corehistory.Store
}

type invalidHistoryStore struct{ err error }

func (i invalidHistoryStore) Read(context.Context, corehistory.ConversationID) ([]chat.Message, error) {
	return nil, i.err
}

func (i invalidHistoryStore) Write(context.Context, corehistory.ConversationID, ...chat.Message) (corehistory.WriteOutcome, error) {
	return corehistory.WriteOutcome{}, i.err
}

func (i invalidHistoryStore) Clear(context.Context, corehistory.ConversationID) error {
	return i.err
}

func (h historyStore) Read(ctx context.Context, conversationID corehistory.ConversationID) ([]chat.Message, error) {
	ctx, observation := h.middleware.start(ctx, operationRead, conversationID)
	messages, err := h.next.Read(ctx, conversationID)
	observation.span.SetAttributes(attribute.Int(messageCountAttribute, len(messages)))
	observation.finish(err)
	return messages, err
}

func (h historyStore) Write(ctx context.Context, conversationID corehistory.ConversationID, messages ...chat.Message) (corehistory.WriteOutcome, error) {
	ctx, observation := h.middleware.start(ctx, operationWrite, conversationID,
		attribute.Int(messageCountAttribute, len(messages)),
	)
	outcome, err := h.next.Write(ctx, conversationID, messages...)
	observation.finish(err)
	return outcome, err
}

func (h historyStore) Clear(ctx context.Context, conversationID corehistory.ConversationID) error {
	ctx, observation := h.middleware.start(ctx, operationClear, conversationID)
	err := h.next.Clear(ctx, conversationID)
	observation.finish(err)
	return err
}

type historyLister struct {
	middleware Middleware
	next       corehistory.Lister
}

type invalidHistoryLister struct{ err error }

func (i invalidHistoryLister) Conversations(context.Context) ([]corehistory.ConversationID, error) {
	return nil, i.err
}

func (h historyLister) Conversations(ctx context.Context) ([]corehistory.ConversationID, error) {
	ctx, observation := h.middleware.start(ctx, operationList, "")
	ids, err := h.next.Conversations(ctx)
	observation.span.SetAttributes(attribute.Int(conversationCountAttribute, len(ids)))
	observation.finish(err)
	return ids, err
}
