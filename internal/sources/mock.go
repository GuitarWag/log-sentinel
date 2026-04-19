package sources

import (
	"context"
	"fmt"
	"math/rand"
	"time"
)

var mockErrorTemplates = []struct {
	severity string
	template string
}{
	{"ERROR", "FATAL: connection to postgres://db:5432/payments timed out after 30s (attempt 3/3)"},
	{"ERROR", "NullPointerException in com.example.OrderService.processPayment(OrderService.java:142)"},
	{"ERROR", "OOM killer terminated process worker-7 (pid 18423): out of memory"},
	{"ERROR", "HTTP 500: upstream service /api/checkout returned 503 after 3 retries"},
	{"ERROR", "panic: runtime error: index out of range [5] with length 3\ngoroutine 47 [running]"},
	{"ERROR", "failed to acquire distributed lock 'order-processing' after 10s: redis SETEX returned nil"},
	{"ERROR", "SSL handshake failed: certificate expired 2026-01-01 (peer: payments.internal:8443)"},
	{"ERROR", "disk write failed on /var/data/logs: no space left on device (used 100%)"},
	{"ERROR", "kafka consumer group lag exceeded threshold: topic=orders partition=3 lag=45231"},
	{"ERROR", "authentication failed for user 'app_user'@'10.0.1.45': Access denied"},
	{"WARN", "slow query detected (4823ms): SELECT * FROM orders WHERE status='pending' LIMIT 10000"},
	{"ERROR", "circuit breaker OPEN for downstream=inventory-service: 15 failures in last 60s"},
	{"ERROR", "S3 PutObject failed: s3://my-bucket/exports/data.csv - RequestTimeout after 30s"},
	{"ERROR", "gRPC call to catalog-service failed: code=UNAVAILABLE desc=connection refused"},
	{"ERROR", "task queue overflow: 10000 pending jobs, dropping oldest 500"},
}

type MockSource struct {
	appName      string
	pollInterval time.Duration
	rng          *rand.Rand
}

func NewMockSource(appName string, pollInterval time.Duration) *MockSource {
	return &MockSource{
		appName:      appName,
		pollInterval: pollInterval,
		rng:          rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (m *MockSource) FetchSince(_ context.Context, _ time.Time) ([]LogEntry, error) {
	count := m.rng.Intn(3) + 1
	entries := make([]LogEntry, 0, count)

	for i := 0; i < count; i++ {
		tpl := mockErrorTemplates[m.rng.Intn(len(mockErrorTemplates))]
		ts := time.Now().Add(-time.Duration(m.rng.Intn(30)) * time.Second)

		rawLine := fmt.Sprintf("[%s] %s %s",
			ts.Format(time.RFC3339),
			tpl.severity,
			tpl.template,
		)

		entries = append(entries, LogEntry{
			AppName:   m.appName,
			Timestamp: ts,
			Severity:  tpl.severity,
			Message:   tpl.template,
			RawLine:   rawLine,
		})
	}

	return entries, nil
}

func (m *MockSource) AppName() string {
	return m.appName
}
