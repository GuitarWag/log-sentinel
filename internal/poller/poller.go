package poller

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/log-sentinel/sentinel/internal/classifier"
	"github.com/log-sentinel/sentinel/internal/config"
	"github.com/log-sentinel/sentinel/internal/queue"
	"github.com/log-sentinel/sentinel/internal/sources"
	"github.com/log-sentinel/sentinel/internal/store"
)

type Classifier interface {
	Classify(ctx context.Context, appName, logLine string) (*classifier.ClassificationResult, error)
}

type Publisher interface {
	Publish(ctx context.Context, msg queue.TicketMessage) (string, error)
}

type TicketStore interface {
	Upsert(ctx context.Context, t *store.Ticket) (*store.UpsertResult, error)
	UpdateSQSMessageID(ctx context.Context, ticketID int64, msgID string) error
	UpdateStatus(ctx context.Context, ticketID int64, status string) error
}

type LogMsg struct {
	Entry sources.LogEntry
}

type TicketMsg struct {
	Ticket *store.Ticket
	IsNew  bool
}

type AppStatusMsg struct {
	AppName  string
	Status   string
	ErrorMsg string
	LastPoll time.Time
}

type Channels struct {
	LogCh    chan<- LogMsg
	TicketCh chan<- TicketMsg
	StatusCh chan<- AppStatusMsg
}

type Poller struct {
	app        config.AppConfig
	source     sources.LogSource
	classifier Classifier
	publisher  Publisher
	db         TicketStore
	channels   Channels
	logger     *slog.Logger
	paused     *atomic.Bool
}

func New(
	app config.AppConfig,
	source sources.LogSource,
	cls Classifier,
	pub Publisher,
	db TicketStore,
	channels Channels,
	paused *atomic.Bool,
) *Poller {
	return &Poller{
		app:        app,
		source:     source,
		classifier: cls,
		publisher:  pub,
		db:         db,
		channels:   channels,
		logger:     slog.Default().With("app", app.Name, "source", app.Source),
		paused:     paused,
	}
}

func (p *Poller) Run(ctx context.Context) {
	interval := p.app.PollInterval.Duration
	if interval <= 0 {
		interval = 30 * time.Second
	}

	p.logger.Info("starting poller", "interval", interval)
	if !p.paused.Load() {
		p.poll(ctx)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("poller stopping due to context cancellation")
			return
		case <-ticker.C:
			if p.paused.Load() {
				continue
			}
			p.poll(ctx)
		}
	}
}

func (p *Poller) poll(ctx context.Context) {
	p.sendStatus("polling", "", time.Time{})

	since := time.Now().Add(-2 * p.app.PollInterval.Duration)
	if p.app.PollInterval.Duration <= 0 {
		since = time.Now().Add(-60 * time.Second)
	}

	entries, err := p.source.FetchSince(ctx, since)
	if err != nil {
		p.logger.Error("fetching logs failed", "error", err)
		p.sendStatus("error", fmt.Sprintf("fetch error: %v", err), time.Now())
		return
	}

	p.sendStatus("idle", "", time.Now())

	for _, entry := range entries {
		if ctx.Err() != nil {
			return
		}

		select {
		case p.channels.LogCh <- LogMsg{Entry: entry}:
		default:
		}

		classifyLine := entry.RawLine
		if entry.DedupeKey != "" {
			classifyLine = entry.DedupeKey
		}
		result, err := p.classifier.Classify(ctx, p.app.Name, classifyLine)
		if err != nil {
			p.logger.Error("classification failed", "error", err, "log", entry.RawLine[:min(len(entry.RawLine), 100)])
			continue
		}

		ticket := &store.Ticket{
			App:             p.app.Name,
			FingerprintHash: result.FingerprintHash,
			FingerprintText: result.Fingerprint,
			Classification:  result.Classification,
			Severity:        result.Severity,
			Component:       result.Component,
			RawLog:          entry.RawLine,
			FirstSeen:       entry.Timestamp,
			LastSeen:        entry.Timestamp,
			Status:          "open",
		}

		upsertResult, err := p.db.Upsert(ctx, ticket)
		if err != nil {
			p.logger.Error("upsert failed", "error", err)
			continue
		}

		if upsertResult.Skipped {
			p.logger.Debug("ticket skipped — already closed today", "fingerprint", ticket.FingerprintText)
			continue
		}

		if upsertResult.IsNew {
			sqsMsg := queue.TicketMessage{
				TicketID:        upsertResult.Ticket.ID,
				AppName:         ticket.App,
				Classification:  ticket.Classification,
				Severity:        ticket.Severity,
				Component:       ticket.Component,
				FingerprintHash: ticket.FingerprintHash,
				FingerprintText: ticket.FingerprintText,
				RawLog:          ticket.RawLog,
				FirstSeen:       ticket.FirstSeen,
			}

			msgID, sqsErr := p.publisher.Publish(ctx, sqsMsg)
			if sqsErr != nil {
				p.logger.Error("SQS publish failed", "error", sqsErr)
				if err := p.db.UpdateStatus(ctx, ticket.ID, "pending"); err != nil {
					p.logger.Error("failed to mark ticket pending", "ticket_id", ticket.ID, "error", err)
				}
				ticket.Status = "pending"
			} else {
				if err := p.db.UpdateSQSMessageID(ctx, ticket.ID, msgID); err != nil {
					p.logger.Error("failed to store sqs message id", "ticket_id", ticket.ID, "error", err)
				}
				ticket.SQSMessageID = msgID
			}
		}

		select {
		case p.channels.TicketCh <- TicketMsg{Ticket: upsertResult.Ticket, IsNew: upsertResult.IsNew}:
		default:
		}
	}
}

func (p *Poller) sendStatus(status, errMsg string, lastPoll time.Time) {
	select {
	case p.channels.StatusCh <- AppStatusMsg{
		AppName:  p.app.Name,
		Status:   status,
		ErrorMsg: errMsg,
		LastPoll: lastPoll,
	}:
	default:
	}
}
