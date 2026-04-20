package classifier

import (
	"context"
	"strings"
)

type MockClassifier struct{}

func NewMockClassifier() *MockClassifier {
	return &MockClassifier{}
}

type rule struct {
	keywords       []string
	classification string
	severity       string
	component      string
	fingerprint    string
}

var rules = []rule{
	{
		keywords:       []string{"postgres", "mysql", "connection", "timeout", "db"},
		classification: "database_connection_timeout",
		severity:       "critical",
		component:      "database",
		fingerprint:    "database connection timeout",
	},
	{
		keywords:       []string{"nullpointerexception", "nil pointer", "null pointer"},
		classification: "null_pointer_exception",
		severity:       "high",
		component:      "application",
		fingerprint:    "null pointer dereference",
	},
	{
		keywords:       []string{"oom", "out of memory", "oom killer"},
		classification: "out_of_memory",
		severity:       "critical",
		component:      "runtime",
		fingerprint:    "process killed by oom killer",
	},
	{
		keywords:       []string{"panic", "runtime error", "index out of range", "goroutine"},
		classification: "runtime_panic",
		severity:       "critical",
		component:      "application",
		fingerprint:    "go runtime panic",
	},
	{
		keywords:       []string{"500", "503", "upstream", "retry"},
		classification: "upstream_service_error",
		severity:       "high",
		component:      "http-client",
		fingerprint:    "upstream service returned 5xx after retries",
	},
	{
		keywords:       []string{"redis", "lock", "distributed lock"},
		classification: "distributed_lock_failure",
		severity:       "high",
		component:      "redis",
		fingerprint:    "failed to acquire distributed lock",
	},
	{
		keywords:       []string{"ssl", "certificate", "tls", "handshake"},
		classification: "tls_certificate_error",
		severity:       "critical",
		component:      "tls",
		fingerprint:    "ssl certificate error",
	},
	{
		keywords:       []string{"disk", "no space", "write failed"},
		classification: "disk_full",
		severity:       "critical",
		component:      "storage",
		fingerprint:    "disk write failed no space left",
	},
	{
		keywords:       []string{"kafka", "consumer", "lag"},
		classification: "kafka_consumer_lag",
		severity:       "high",
		component:      "kafka",
		fingerprint:    "kafka consumer lag exceeded threshold",
	},
	{
		keywords:       []string{"authentication", "access denied", "auth failed"},
		classification: "authentication_failure",
		severity:       "medium",
		component:      "auth",
		fingerprint:    "database authentication failure",
	},
	{
		keywords:       []string{"slow query", "4823ms", "query"},
		classification: "slow_database_query",
		severity:       "medium",
		component:      "database",
		fingerprint:    "slow database query detected",
	},
	{
		keywords:       []string{"circuit breaker", "open", "failures"},
		classification: "circuit_breaker_open",
		severity:       "high",
		component:      "circuit-breaker",
		fingerprint:    "circuit breaker opened for downstream service",
	},
	{
		keywords:       []string{"s3", "putobject", "requesttimeout"},
		classification: "s3_timeout",
		severity:       "high",
		component:      "s3",
		fingerprint:    "s3 put object request timeout",
	},
	{
		keywords:       []string{"grpc", "unavailable", "connection refused"},
		classification: "grpc_connection_refused",
		severity:       "high",
		component:      "grpc",
		fingerprint:    "grpc call failed connection refused",
	},
	{
		keywords:       []string{"queue overflow", "pending jobs", "dropping"},
		classification: "task_queue_overflow",
		severity:       "high",
		component:      "task-queue",
		fingerprint:    "task queue overflow jobs dropped",
	},
}

func (m *MockClassifier) Classify(_ context.Context, appName, logLine string) (*ClassificationResult, error) {
	lower := strings.ToLower(logLine)

	for _, r := range rules {
		for _, kw := range r.keywords {
			if strings.Contains(lower, kw) {
				result := &ClassificationResult{
					Classification:  r.classification,
					Severity:        r.severity,
					Component:       r.component,
					Fingerprint:     r.fingerprint,
					FingerprintHash: normalizedHash(appName, logLine),
				}
				return result, nil
			}
		}
	}

	result := &ClassificationResult{
		Classification:  "unknown_error",
		Severity:        "medium",
		Component:       "unknown",
		Fingerprint:     "unclassified error",
		FingerprintHash: normalizedHash(appName, logLine),
	}
	return result, nil
}
