package repository_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/rootless-dev/aegis/internal/repository"
	"gorm.io/gorm"
)

func TestTimestampScansEveryShapeTheDriversReturn(t *testing.T) {
	want := time.Date(2026, 8, 25, 12, 34, 56, 123456000, time.UTC)

	cases := []struct {
		name string
		src  any
	}{
		{"time.Time from postgres and mysql", want},
		{"string from a sqlite TEXT column", "2026-08-25T12:34:56.123456Z"},
		{"bytes from a driver that returns raw text", []byte("2026-08-25T12:34:56.123456Z")},
	}

	for _, tc := range cases {
		var got repository.Timestamp

		if err := got.Scan(tc.src); err != nil {
			t.Errorf("%s: %v", tc.name, err)

			continue
		}

		if !time.Time(got).Equal(want) {
			t.Errorf("%s: want %s, got %s", tc.name, want, time.Time(got))
		}

		if time.Time(got).Location() != time.UTC {
			t.Errorf("%s: must come back in UTC", tc.name)
		}
	}
}

// A zero time on a nonsense value would look like a very old row rather than a
// problem, and nothing downstream could tell the difference.
func TestTimestampRefusesWhatItCannotRead(t *testing.T) {
	var ts repository.Timestamp

	for _, src := range []any{42, "not a time", nil} {
		if err := ts.Scan(src); err == nil {
			t.Errorf("%v must be an error, not a zero time", src)
		}
	}
}

// Value returns a time.Time rather than a formatted string: every driver
// already knows what to do with one, and formatting by hand would mean
// fighting each dialect's literal syntax.
func TestTimestampRoundTripsThroughAStrictTextColumn(t *testing.T) {
	db, err := gorm.Open(
		gormlite.Open("file:"+filepath.Join(t.TempDir(), "probe.db")),
		&gorm.Config{SkipDefaultTransaction: true},
	)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}

	create := `CREATE TABLE probe (
		id TEXT NOT NULL,
		at TEXT NOT NULL,
		CONSTRAINT pk_probe PRIMARY KEY (id)
	) STRICT`

	if err := db.Exec(create).Error; err != nil {
		t.Fatalf("creating: %v", err)
	}

	type probe struct {
		ID string               `gorm:"column:id;primaryKey"`
		At repository.Timestamp `gorm:"column:at"`
	}

	want := time.Date(2026, 8, 25, 12, 34, 56, 123456000, time.UTC)

	if err := db.Table("probe").Create(&probe{ID: "x", At: repository.Timestamp(want)}).Error; err != nil {
		t.Fatalf("inserting: %v", err)
	}

	var stored, kind string

	db.Raw(`SELECT at FROM probe`).Scan(&stored)
	db.Raw(`SELECT typeof(at) FROM probe`).Scan(&kind)

	if kind != "text" {
		t.Errorf("a STRICT TEXT column must hold text, got %q", kind)
	}

	if stored != "2026-08-25T12:34:56.123456Z" {
		t.Errorf("stored: got %q", stored)
	}

	var got probe

	if err := db.Table("probe").First(&got).Error; err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if !time.Time(got.At).Equal(want) {
		t.Errorf("round trip: want %s, got %s", want, time.Time(got.At))
	}
}
