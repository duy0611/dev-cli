package store

import (
	"errors"
	"time"
)

// ErrNotFound is returned by every Get for a row that is not there. Callers
// turn it into the command-level "no such thing" message and exit code.
var ErrNotFound = errors.New("not found")

// ErrExists is returned when a create would collide with an existing row.
var ErrExists = errors.New("already exists")

// Timestamps are written by the application rather than by the database, so
// that the same SQL runs unchanged against Postgres. RFC 3339 in UTC sorts
// lexically, which keeps ORDER BY created_at honest.
const timeFormat = time.RFC3339Nano

func nowString() string { return time.Now().UTC().Format(timeFormat) }

func parseTime(s string) (time.Time, error) { return time.Parse(timeFormat, s) }
