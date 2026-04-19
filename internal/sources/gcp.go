package sources

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/logging/apiv2/loggingpb"
	"google.golang.org/api/iterator"

	logadmin "cloud.google.com/go/logging/apiv2"
)

type GCPSource struct {
	appName string
	project string
	filter  string
	client  *logadmin.Client
}

func NewGCPSource(ctx context.Context, appName, project, filter string) (*GCPSource, error) {
	client, err := logadmin.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("creating GCP logging client: %w", err)
	}

	return &GCPSource{
		appName: appName,
		project: project,
		filter:  filter,
		client:  client,
	}, nil
}

func (g *GCPSource) AppName() string {
	return g.appName
}

func (g *GCPSource) FetchSince(ctx context.Context, since time.Time) ([]LogEntry, error) {
	timeFilter := fmt.Sprintf(`timestamp > "%s"`, since.UTC().Format(time.RFC3339))

	combinedFilter := timeFilter
	if g.filter != "" {
		combinedFilter = fmt.Sprintf("(%s) AND (%s)", g.filter, timeFilter)
	}

	req := &loggingpb.ListLogEntriesRequest{
		ResourceNames: []string{fmt.Sprintf("projects/%s", g.project)},
		Filter:        combinedFilter,
		OrderBy:       "timestamp asc",
		PageSize:      500,
	}

	it := g.client.ListLogEntries(ctx, req)
	var entries []LogEntry

	for {
		logEntry, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("iterating GCP log entries: %w", err)
		}

		entry := logEntry
		ts := entry.GetTimestamp().AsTime()
		severity := entry.GetSeverity().String()
		message := extractGCPMessage(entry)

		entries = append(entries, LogEntry{
			AppName:   g.appName,
			Timestamp: ts,
			Severity:  severity,
			Message:   message,
			RawLine:   fmt.Sprintf("[%s] %s %s", ts.Format(time.RFC3339), severity, message),
		})
	}

	return entries, nil
}

func extractGCPMessage(entry *loggingpb.LogEntry) string {
	if entry == nil {
		return ""
	}

	if text := entry.GetTextPayload(); text != "" {
		return text
	}

	if jsonPayload := entry.GetJsonPayload(); jsonPayload != nil {
		fields := jsonPayload.GetFields()
		for _, key := range []string{"message", "msg", "log", "error", "err"} {
			if v, ok := fields[key]; ok {
				return v.GetStringValue()
			}
		}
		return fmt.Sprintf("[JSON payload with %d fields]", len(fields))
	}

	if protoPayload := entry.GetProtoPayload(); protoPayload != nil {
		return fmt.Sprintf("[proto payload: %s]", protoPayload.TypeUrl)
	}

	return "[empty log entry]"
}

func (g *GCPSource) Close() error {
	return g.client.Close()
}
