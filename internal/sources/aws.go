package sources

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

type AWSSource struct {
	appName       string
	logGroup      string
	filterPattern string
	region        string
	client        *cloudwatchlogs.Client
}

func NewAWSSource(appName, logGroup, filterPattern, region string, cfg aws.Config) *AWSSource {
	client := cloudwatchlogs.NewFromConfig(cfg, func(o *cloudwatchlogs.Options) {
		if region != "" {
			o.Region = region
		}
	})

	return &AWSSource{
		appName:       appName,
		logGroup:      logGroup,
		filterPattern: filterPattern,
		region:        region,
		client:        client,
	}
}

func (a *AWSSource) AppName() string {
	return a.appName
}

func (a *AWSSource) FetchSince(ctx context.Context, since time.Time) ([]LogEntry, error) {
	input := &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String(a.logGroup),
		StartTime:    aws.Int64(since.UnixMilli()),
		Limit:        aws.Int32(500),
	}

	if a.filterPattern != "" {
		input.FilterPattern = aws.String(a.filterPattern)
	}

	var entries []LogEntry
	paginator := cloudwatchlogs.NewFilterLogEventsPaginator(a.client, input)

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("fetching CloudWatch log events for %q: %w", a.logGroup, err)
		}

		for _, event := range page.Events {
			entries = append(entries, convertCloudWatchEvent(a.appName, event))
		}
	}

	return entries, nil
}

func convertCloudWatchEvent(appName string, event types.FilteredLogEvent) LogEntry {
	var ts time.Time
	if event.Timestamp != nil {
		ts = time.UnixMilli(*event.Timestamp).UTC()
	}

	message := ""
	if event.Message != nil {
		message = *event.Message
	}

	severity := inferSeverity(message)

	return LogEntry{
		AppName:   appName,
		Timestamp: ts,
		Severity:  severity,
		Message:   message,
		RawLine:   fmt.Sprintf("[%s] %s %s", ts.Format(time.RFC3339), severity, message),
	}
}

func inferSeverity(message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "critical") || strings.Contains(lower, "fatal") || strings.Contains(lower, "panic"):
		return "CRITICAL"
	case strings.Contains(lower, "error") || strings.Contains(lower, "exception") || strings.Contains(lower, "fail"):
		return "ERROR"
	case strings.Contains(lower, "warn") || strings.Contains(lower, "warning"):
		return "WARNING"
	default:
		return "ERROR"
	}
}
