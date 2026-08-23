package database

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/phuslu/log"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// secretValue stands in for a credential, hash or token.
const secretValue = "hunter2-should-never-appear"

func captureLogger() (*log.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}

	return &log.Logger{Level: log.DebugLevel, Writer: log.IOWriter{Writer: buf}}, buf
}

// explain reproduces what GORM's callback does before calling Trace: Trace
// never calls ParamsFilter itself, so exercising it alone would prove nothing
// about redaction.
func explain(g *gormLogger, sql string, args ...any) func() (string, int64) {
	return func() (string, int64) {
		filteredSQL, filteredArgs := g.ParamsFilter(context.Background(), sql, args...)

		return gormlogger.ExplainSQL(filteredSQL, nil, `"`, filteredArgs...), 1
	}
}

// A production configuration must never let a secret reach the log.
func TestTraceRedactsParametersWhenLogParametersIsOff(t *testing.T) {
	logger, buf := captureLogger()
	adapter := newGormLogger(logger, 0, true).(*gormLogger)

	adapter.Trace(context.Background(), time.Now(), explain(adapter, "SELECT * FROM users WHERE token = ?", secretValue), nil)

	output := buf.String()
	if strings.Contains(output, secretValue) {
		t.Fatalf("expected the secret to be redacted, got %q", output)
	}

	if !strings.Contains(output, "token = ?") {
		t.Fatalf("expected the placeholder to remain in the logged statement, got %q", output)
	}
}

// The counterpart: a filter that redacted unconditionally would pass the test
// above and make development logs useless.
func TestTraceIncludesParametersWhenLogParametersIsOn(t *testing.T) {
	logger, buf := captureLogger()
	adapter := newGormLogger(logger, 0, false).(*gormLogger)

	adapter.Trace(context.Background(), time.Now(), explain(adapter, "SELECT * FROM users WHERE token = ?", secretValue), nil)

	output := buf.String()
	if !strings.Contains(output, secretValue) {
		t.Fatalf("expected the secret to appear when LogParameters is enabled, got %q", output)
	}
}

func TestParamsFilterStripsParamsOnlyWhenParameterized(t *testing.T) {
	adapter := &gormLogger{parameterizedQueries: true}

	if _, params := adapter.ParamsFilter(context.Background(), "SELECT ?", secretValue); params != nil {
		t.Fatalf("expected params to be stripped when parameterized, got %v", params)
	}

	adapter.parameterizedQueries = false

	if _, params := adapter.ParamsFilter(context.Background(), "SELECT ?", secretValue); len(params) != 1 || params[0] != secretValue {
		t.Fatalf("expected the original params to be preserved when not parameterized, got %v", params)
	}
}

// fc renders the whole statement through the dialector, so a healthy instance
// must not call it once per query only to discard the line.
func TestTraceRendersTheStatementOnlyWhenItWouldBeLogged(t *testing.T) {
	logger := &log.Logger{Level: log.InfoLevel, Writer: log.IOWriter{Writer: io.Discard}}
	adapter := newGormLogger(logger, time.Millisecond, true).(*gormLogger)

	renders := 0
	fc := func() (string, int64) {
		renders++

		return "SELECT 1", 1
	}

	adapter.Trace(context.Background(), time.Now(), fc, nil)

	if renders != 0 {
		t.Fatalf("expected a routine query to render nothing below debug, got %d renders", renders)
	}

	// A missing row is the other routine outcome, and takes the same path.
	adapter.Trace(context.Background(), time.Now(), fc, gorm.ErrRecordNotFound)

	if renders != 0 {
		t.Fatalf("expected a missing row to render nothing below debug, got %d renders", renders)
	}

	adapter.Trace(context.Background(), time.Now(), fc, errors.New("boom"))

	if renders != 1 {
		t.Fatalf("expected a failed query to be rendered, got %d renders", renders)
	}

	adapter.Trace(context.Background(), time.Now().Add(-time.Second), fc, nil)

	if renders != 2 {
		t.Fatalf("expected a slow query to be rendered, got %d renders", renders)
	}
}

// And at debug every statement still gets through.
func TestTraceRendersRoutineStatementsAtDebug(t *testing.T) {
	logger, buf := captureLogger()
	adapter := newGormLogger(logger, 0, true).(*gormLogger)

	adapter.Trace(context.Background(), time.Now(), func() (string, int64) { return "SELECT 1", 1 }, nil)

	if !strings.Contains(buf.String(), "SELECT 1") {
		t.Fatalf("expected the routine statement to be logged at debug, got %q", buf.String())
	}
}

// A caller that left Logger unset gets the default one, not a nil deref on the
// first statement or on the announcement Open makes.
func TestOptionsFallBackToTheDefaultLogger(t *testing.T) {
	if (Options{}).logger() == nil {
		t.Fatal("expected a usable logger when Options carries none")
	}

	opts := sqliteOptions(t)
	opts.Logger = nil

	db, err := Open(opts)
	if err != nil {
		t.Fatalf("opening without a logger: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })
}
