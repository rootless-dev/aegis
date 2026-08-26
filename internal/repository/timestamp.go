package repository

import (
	"database/sql/driver"
	"fmt"
	"time"
)

// Timestamp is what every record uses for a time column. A plain time.Time
// does not survive the round trip on SQLite: the driver hands a TEXT column
// back as a string, which database/sql will not scan into a *time.Time.
type Timestamp time.Time

// A time.Time rather than a formatted string: every driver already handles one,
// and formatting by hand would mean fighting each dialect's literal syntax.
func (t Timestamp) Value() (driver.Value, error) {
	return time.Time(t).UTC().Truncate(time.Microsecond), nil
}

// Tolerates what the drivers return: time.Time from Postgres and MySQL, text
// from SQLite.
func (t *Timestamp) Scan(src any) error {
	switch value := src.(type) {
	case time.Time:
		*t = Timestamp(value.UTC())

		return nil
	case string:
		return t.parse(value)
	case []byte:
		return t.parse(string(value))
	default:
		// An error rather than a zero time, which would read as a very old row.
		return fmt.Errorf("repository: cannot read a timestamp out of %T", src)
	}
}

func (t *Timestamp) parse(raw string) error {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return fmt.Errorf("repository: cannot read a timestamp out of %q: %w", raw, err)
	}

	*t = Timestamp(parsed.UTC())

	return nil
}

func (t Timestamp) Time() time.Time { return time.Time(t).UTC() }

// Timestamps come from here rather than DEFAULT CURRENT_TIMESTAMP, whose
// precision and ON UPDATE behaviour differ across the four engines.
func nowUTC() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
