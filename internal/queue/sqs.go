package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

type TicketMessage struct {
	TicketID        int64     `json:"ticket_id"`
	AppName         string    `json:"app_name"`
	Classification  string    `json:"classification"`
	Severity        string    `json:"severity"`
	Component       string    `json:"component"`
	FingerprintHash string    `json:"fingerprint_hash"`
	FingerprintText string    `json:"fingerprint_text"`
	RawLog          string    `json:"raw_log"`
	FirstSeen       time.Time `json:"first_seen"`
}

type Publisher struct {
	client   *sqs.Client
	queueURL string
}

func NewPublisher(awsCfg aws.Config, queueURL string) *Publisher {
	client := sqs.NewFromConfig(awsCfg)
	return &Publisher{
		client:   client,
		queueURL: queueURL,
	}
}

func (p *Publisher) Publish(ctx context.Context, msg TicketMessage) (string, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("marshaling SQS message: %w", err)
	}

	input := &sqs.SendMessageInput{
		QueueUrl:    aws.String(p.queueURL),
		MessageBody: aws.String(string(body)),
		MessageAttributes: map[string]sqstypes.MessageAttributeValue{
			"AppName": {
				DataType:    aws.String("String"),
				StringValue: aws.String(msg.AppName),
			},
			"Severity": {
				DataType:    aws.String("String"),
				StringValue: aws.String(msg.Severity),
			},
			"Classification": {
				DataType:    aws.String("String"),
				StringValue: aws.String(msg.Classification),
			},
		},
	}

	output, err := p.client.SendMessage(ctx, input)
	if err != nil {
		return "", fmt.Errorf("sending SQS message: %w", err)
	}

	if output.MessageId == nil {
		return "", fmt.Errorf("SQS returned nil message ID")
	}

	return *output.MessageId, nil
}
