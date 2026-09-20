package telemetry

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
)

const gormSpanStateKey = "paon:opentelemetry:span"

type gormSpanState struct {
	context   context.Context
	span      trace.Span
	operation string
	started   time.Time
}

// InstrumentGORM adds redacted GORM callbacks. SQL text, bind values, database
// URLs, table names and record data are intentionally not collected.
func InstrumentGORM(database *gorm.DB) error {
	if database == nil || !Enabled() {
		return nil
	}
	var registrationErrors []error
	registrationErrors = append(registrationErrors,
		database.Callback().Create().Before("*").Register("paon:otel_before_create", startGORMOperation("INSERT")),
		database.Callback().Create().After("*").Register("paon:otel_after_create", finishGORMOperation),
		database.Callback().Query().Before("*").Register("paon:otel_before_query", startGORMOperation("SELECT")),
		database.Callback().Query().After("*").Register("paon:otel_after_query", finishGORMOperation),
		database.Callback().Update().Before("*").Register("paon:otel_before_update", startGORMOperation("UPDATE")),
		database.Callback().Update().After("*").Register("paon:otel_after_update", finishGORMOperation),
		database.Callback().Delete().Before("*").Register("paon:otel_before_delete", startGORMOperation("DELETE")),
		database.Callback().Delete().After("*").Register("paon:otel_after_delete", finishGORMOperation),
		database.Callback().Row().Before("*").Register("paon:otel_before_row", startGORMOperation("SELECT")),
		database.Callback().Row().After("*").Register("paon:otel_after_row", finishGORMOperation),
		database.Callback().Raw().Before("*").Register("paon:otel_before_raw", startGORMOperation("RAW")),
		database.Callback().Raw().After("*").Register("paon:otel_after_raw", finishGORMOperation),
	)
	return errors.Join(registrationErrors...)
}

func startGORMOperation(operation string) func(*gorm.DB) {
	operation = strings.ToUpper(safeLabel(operation))
	return func(database *gorm.DB) {
		if database == nil || database.Statement == nil {
			return
		}
		ctx, span := tracer().Start(database.Statement.Context, "postgresql "+operation,
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("db.system.name", "postgresql"),
				attribute.String("db.operation.name", operation),
			),
		)
		database.Statement.Context = ctx
		database.Statement.Settings.Store(gormSpanStateKey, gormSpanState{
			context:   ctx,
			span:      span,
			operation: operation,
			started:   time.Now(),
		})
	}
}

func finishGORMOperation(database *gorm.DB) {
	if database == nil || database.Statement == nil {
		return
	}
	value, ok := database.Statement.Settings.LoadAndDelete(gormSpanStateKey)
	if !ok {
		return
	}
	state, ok := value.(gormSpanState)
	if !ok || state.span == nil {
		return
	}
	outcome := "success"
	if database.Error != nil && !errors.Is(database.Error, gorm.ErrRecordNotFound) {
		outcome = "failure"
		state.span.SetStatus(codes.Error, "database operation failed")
	}
	attrs := []attribute.KeyValue{
		attribute.String("db.operation.name", state.operation),
		attribute.String("paon.outcome", outcome),
	}
	m := metrics()
	m.dbDuration.Record(state.context, time.Since(state.started).Seconds(), metric.WithAttributes(attrs...))
	m.dbOperations.Add(state.context, 1, metric.WithAttributes(attrs...))
	state.span.End()
}
