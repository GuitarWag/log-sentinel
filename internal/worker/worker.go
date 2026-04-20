package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/log-sentinel/sentinel/internal/config"
	"github.com/log-sentinel/sentinel/internal/store"
)

type TicketStore interface {
	UpdateStatus(ctx context.Context, ticketID int64, status string) error
}

const (
	longPollSeconds   = 20
	visibilityTimeout = 300
	maxMessages       = 1
)

type Event struct {
	TicketID       int64
	App            string
	ActionName     string
	Classification string
	Severity       string
	Status         string
	ErrMsg         string
	Output         string
	Timestamp      time.Time
}

type SQSClient interface {
	ReceiveMessage(ctx context.Context, params *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, params *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, params *sqs.ChangeMessageVisibilityInput, optFns ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
}

type Worker struct {
	cfg      config.WorkerConfig
	queueURL string
	sqs      SQSClient
	db       TicketStore
	actions  []Action
	eventCh  chan<- Event
	logger   *slog.Logger
}

func New(cfg config.WorkerConfig, queueURL string, sqsClient SQSClient, db TicketStore, eventCh chan<- Event) (*Worker, error) {
	actions := make([]Action, 0, len(cfg.Actions))
	for i, ac := range cfg.Actions {
		a, err := BuildAction(ac)
		if err != nil {
			return nil, fmt.Errorf("worker %q action[%d]: %w", cfg.App, i, err)
		}
		actions = append(actions, a)
	}

	return &Worker{
		cfg:      cfg,
		queueURL: queueURL,
		sqs:      sqsClient,
		db:       db,
		actions:  actions,
		eventCh:  eventCh,
		logger:   slog.Default().With("worker", cfg.App),
	}, nil
}

func (w *Worker) Run(ctx context.Context) {
	concurrency := w.cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}

	w.logger.Info("starting worker", "concurrency", concurrency, "actions", len(w.actions))

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			w.loop(ctx, id)
		}(i)
	}
	wg.Wait()
}

func (w *Worker) loop(ctx context.Context, goroutineID int) {
	log := w.logger.With("goroutine", goroutineID)
	for {
		if ctx.Err() != nil {
			return
		}

		out, err := w.sqs.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:              aws.String(w.queueURL),
			MaxNumberOfMessages:   maxMessages,
			WaitTimeSeconds:       longPollSeconds,
			VisibilityTimeout:     visibilityTimeout,
			MessageAttributeNames: []string{"AppName", "Severity", "Classification"},
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Error("sqs receive failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		for _, msg := range out.Messages {
			if ctx.Err() != nil {
				return
			}
			w.process(ctx, log, msg)
		}
	}
}

func (w *Worker) process(ctx context.Context, log *slog.Logger, msg sqstypes.Message) {
	if msg.Body == nil || msg.ReceiptHandle == nil {
		return
	}

	ticket, err := ParseTicket(*msg.Body)
	if err != nil {
		log.Error("unparseable ticket, discarding", "error", err, "body", *msg.Body)
		w.deleteMessage(ctx, *msg.ReceiptHandle)
		return
	}

	if attrVal, ok := msg.MessageAttributes["AppName"]; ok {
		if attrVal.StringValue != nil && *attrVal.StringValue != w.cfg.App {
			w.changeVisibility(ctx, *msg.ReceiptHandle, 0)
			return
		}
	}

	log.Info("processing ticket",
		"app", ticket.AppName,
		"classification", ticket.Classification,
		"severity", ticket.Severity,
		"fingerprint", ticket.FingerprintText,
	)

	if ticket.TicketID > 0 {
		w.updateStatus(ctx, ticket.TicketID, store.StatusInProgress)
	}

	for _, action := range w.actions {
		if ctx.Err() != nil {
			return
		}
		w.sendEvent(Event{
			TicketID:       ticket.TicketID,
			App:            ticket.AppName,
			ActionName:     action.Name(),
			Classification: ticket.Classification,
			Severity:       ticket.Severity,
			Status:         "processing",
			Timestamp:      time.Now(),
		})
		output, err := action.Run(ctx, ticket)
		if err != nil {
			log.Error("action failed, leaving message for redelivery",
				"action", action.Name(),
				"error", err,
			)
			w.sendEvent(Event{
				TicketID:       ticket.TicketID,
				App:            ticket.AppName,
				ActionName:     action.Name(),
				Classification: ticket.Classification,
				Severity:       ticket.Severity,
				Status:         "failed",
				ErrMsg:         err.Error(),
				Timestamp:      time.Now(),
			})
			if ticket.TicketID > 0 {
				w.updateStatus(ctx, ticket.TicketID, store.StatusFailed)
			}
			w.changeVisibility(ctx, *msg.ReceiptHandle, 30)
			return
		}
		w.sendEvent(Event{
			TicketID:       ticket.TicketID,
			App:            ticket.AppName,
			ActionName:     action.Name(),
			Classification: ticket.Classification,
			Severity:       ticket.Severity,
			Status:         "done",
			Output:         output,
			Timestamp:      time.Now(),
		})
		log.Info("action completed", "action", action.Name())
	}

	if ticket.TicketID > 0 {
		w.updateStatus(ctx, ticket.TicketID, store.StatusDone)
	}
	w.deleteMessage(ctx, *msg.ReceiptHandle)
	log.Info("ticket processed and acknowledged",
		"app", ticket.AppName,
		"classification", ticket.Classification,
	)
}

func (w *Worker) deleteMessage(ctx context.Context, receiptHandle string) {
	_, err := w.sqs.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(w.queueURL),
		ReceiptHandle: aws.String(receiptHandle),
	})
	if err != nil {
		w.logger.Error("failed to delete sqs message", "error", err)
	}
}

func (w *Worker) updateStatus(ctx context.Context, ticketID int64, status string) {
	if w.db == nil {
		return
	}
	if err := w.db.UpdateStatus(ctx, ticketID, status); err != nil {
		w.logger.Error("failed to update ticket status", "ticket_id", ticketID, "status", status, "error", err)
	}
}

func (w *Worker) sendEvent(e Event) {
	if w.eventCh == nil {
		return
	}
	select {
	case w.eventCh <- e:
	default:
	}
}

func (w *Worker) changeVisibility(ctx context.Context, receiptHandle string, seconds int32) {
	_, err := w.sqs.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(w.queueURL),
		ReceiptHandle:     aws.String(receiptHandle),
		VisibilityTimeout: seconds,
	})
	if err != nil {
		w.logger.Error("failed to change message visibility", "error", err)
	}
}
