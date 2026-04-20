package tui

import (
	"context"
	"log/slog"
	"time"
)

type SentinelLogRecord struct {
	Level   slog.Level
	Time    time.Time
	Message string
	Attrs   []slog.Attr
}

type fanOutHandler struct {
	file    slog.Handler
	ch      chan<- SentinelLogRecord
	prelude []slog.Attr
}

func NewFanOutHandler(file slog.Handler, ch chan<- SentinelLogRecord) slog.Handler {
	return &fanOutHandler{file: file, ch: ch}
}

func (h *fanOutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.file.Enabled(ctx, level)
}

func (h *fanOutHandler) Handle(ctx context.Context, r slog.Record) error {
	attrs := make([]slog.Attr, 0, len(h.prelude)+r.NumAttrs())
	attrs = append(attrs, h.prelude...)
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})

	rec := SentinelLogRecord{
		Level:   r.Level,
		Time:    r.Time,
		Message: r.Message,
		Attrs:   attrs,
	}

	select {
	case h.ch <- rec:
	default:
	}

	return h.file.Handle(ctx, r)
}

func (h *fanOutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, len(h.prelude)+len(attrs))
	copy(merged, h.prelude)
	copy(merged[len(h.prelude):], attrs)
	return &fanOutHandler{file: h.file.WithAttrs(attrs), ch: h.ch, prelude: merged}
}

func (h *fanOutHandler) WithGroup(name string) slog.Handler {
	return &fanOutHandler{file: h.file.WithGroup(name), ch: h.ch, prelude: h.prelude}
}
