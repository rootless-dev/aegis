package database

import (
	"context"
	"errors"
	"time"

	"github.com/phuslu/log"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// gormLogger routes GORM through the application's logger. Left alone, GORM
// prints to stdout with log.Printf: unstructured, and invisible to whatever
// collects logs at the customer's site.
type gormLogger struct {
	logger        *log.Logger
	level         gormlogger.LogLevel
	slowThreshold time.Duration

	// parameterizedQueries strips query arguments before they reach the log:
	// they are credentials, tokens and personal data.
	parameterizedQueries bool
}

func newGormLogger(logger *log.Logger, slowThreshold time.Duration, parameterizedQueries bool) gormlogger.Interface {
	return &gormLogger{
		logger:               logger,
		level:                gormlogger.Warn,
		slowThreshold:        slowThreshold,
		parameterizedQueries: parameterizedQueries,
	}
}

// ParamsFilter is picked up by GORM through a type assertion. Nil params leave
// the statement with its placeholders.
func (g *gormLogger) ParamsFilter(_ context.Context, sql string, params ...any) (string, []any) {
	if g.parameterizedQueries {
		return sql, nil
	}

	return sql, params
}

func (g *gormLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	clone := *g
	clone.level = level

	return &clone
}

func (g *gormLogger) Info(_ context.Context, message string, data ...any) {
	if g.level >= gormlogger.Info {
		g.logger.Info().Msgf(message, data...)
	}
}

func (g *gormLogger) Warn(_ context.Context, message string, data ...any) {
	if g.level >= gormlogger.Warn {
		g.logger.Warn().Msgf(message, data...)
	}
}

func (g *gormLogger) Error(_ context.Context, message string, data ...any) {
	if g.level >= gormlogger.Error {
		g.logger.Error().Msgf(message, data...)
	}
}

// Trace runs once per statement. fc is called inside each branch and never
// ahead of them: it renders the whole statement through Dialector.Explain, and
// a healthy instance would pay that on every query only to discard the line.
func (g *gormLogger) Trace(_ context.Context, begin time.Time, fc func() (string, int64), err error) {
	if g.level <= gormlogger.Silent {
		return
	}

	elapsed := time.Since(begin)

	switch {
	// A missing row is an ordinary lookup outcome, not a database error.
	case err != nil && !errors.Is(err, gorm.ErrRecordNotFound):
		statement, rows := fc()
		g.logger.Error().Err(err).Str("sql", statement).Int64("rows", rows).Dur("elapsed", elapsed).Msg("query failed")
	case g.slowThreshold > 0 && elapsed > g.slowThreshold:
		statement, rows := fc()
		g.logger.Warn().Str("sql", statement).Int64("rows", rows).Dur("elapsed", elapsed).Msg("slow query")
	case g.logsDebug():
		statement, rows := fc()
		g.logger.Debug().Str("sql", statement).Int64("rows", rows).Dur("elapsed", elapsed).Msg("query")
	}
}

// logsDebug asks the level directly: going through Debug() would take an entry
// out of the logger's pool only to discard it.
func (g *gormLogger) logsDebug() bool {
	return log.DebugLevel >= g.logger.Level
}
