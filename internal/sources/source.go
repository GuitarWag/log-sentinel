package sources

import (
	"context"
	"time"
)

type LogEntry struct {
	AppName   string
	Timestamp time.Time
	Severity  string
	Message   string
	RawLine   string
}

type LogSource interface {
	FetchSince(ctx context.Context, since time.Time) ([]LogEntry, error)
	AppName() string
}
