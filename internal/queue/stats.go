package queue

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

type QueueStats struct {
	ApproxMessages int
	InFlight       int
}

type StatsWatcher struct {
	client   *sqs.Client
	queueURL string
	ch       chan<- QueueStats
}

func NewStatsWatcher(awsCfg aws.Config, queueURL string, ch chan<- QueueStats) *StatsWatcher {
	return &StatsWatcher{
		client:   sqs.NewFromConfig(awsCfg),
		queueURL: queueURL,
		ch:       ch,
	}
}

func (w *StatsWatcher) Run(ctx context.Context) {
	w.poll(ctx)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.poll(ctx)
		}
	}
}

func (w *StatsWatcher) poll(ctx context.Context) {
	out, err := w.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(w.queueURL),
		AttributeNames: []sqstypes.QueueAttributeName{
			sqstypes.QueueAttributeNameApproximateNumberOfMessages,
			sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	})
	if err != nil {
		slog.Debug("sqs stats poll failed", "error", err)
		return
	}
	stats := QueueStats{}
	if v, ok := out.Attributes["ApproximateNumberOfMessages"]; ok {
		stats.ApproxMessages, _ = strconv.Atoi(v)
	}
	if v, ok := out.Attributes["ApproximateNumberOfMessagesNotVisible"]; ok {
		stats.InFlight, _ = strconv.Atoi(v)
	}
	select {
	case w.ch <- stats:
	default:
	}
}
