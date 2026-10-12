package store

import (
	"errors"
	"time"
)

// ErrNotFound is returned by every Get for a row that is not there.
var ErrNotFound = errors.New("not found")

// ErrExists is returned when a create would collide with an existing row.
var ErrExists = errors.New("already exists")

// Timestamps are written by the application, not the database, so the same SQL
// runs against Postgres. RFC 3339 in UTC sorts lexically.
const timeFormat = time.RFC3339Nano

func nowString() string { return time.Now().UTC().Format(timeFormat) }

func parseTime(s string) (time.Time, error) { return time.Parse(timeFormat, s) }
