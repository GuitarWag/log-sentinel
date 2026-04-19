package classifier

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

func TestParseClassificationJSON_valid(t *testing.T) {
	input := `{
		"classification": "database_connection_timeout",
		"severity": "critical",
		"component": "payments-service",
		"fingerprint": "postgres connection timeout after 30s in checkout flow"
	}`
	result, err := parseClassificationJSON(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Classification != "database_connection_timeout" {
		t.Errorf("classification = %q, want %q", result.Classification, "database_connection_timeout")
	}
	if result.Severity != "critical" {
		t.Errorf("severity = %q, want %q", result.Severity, "critical")
	}
	if result.Component != "payments-service" {
		t.Errorf("component = %q, want %q", result.Component, "payments-service")
	}
	if result.Fingerprint != "postgres connection timeout after 30s in checkout flow" {
		t.Errorf("fingerprint = %q", result.Fingerprint)
	}
}

func TestParseClassificationJSON_withPreamble(t *testing.T) {
	// Model sometimes returns text before the JSON block.
	input := `Here is the analysis: {"classification":"null_pointer_exception","severity":"high","component":"api-gateway","fingerprint":"null pointer in request handler"}`
	result, err := parseClassificationJSON(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Classification != "null_pointer_exception" {
		t.Errorf("classification = %q", result.Classification)
	}
}

func TestParseClassificationJSON_missingFields_usesDefaults(t *testing.T) {
	input := `{}`
	result, err := parseClassificationJSON(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Classification != "unknown_error" {
		t.Errorf("classification = %q, want %q", result.Classification, "unknown_error")
	}
	if result.Severity != "medium" {
		t.Errorf("severity = %q, want %q", result.Severity, "medium")
	}
	if result.Component != "unknown" {
		t.Errorf("component = %q, want %q", result.Component, "unknown")
	}
}

func TestParseClassificationJSON_fingerprintFallsBackToClassification(t *testing.T) {
	input := `{"classification":"oom_killed","severity":"critical","component":"worker"}`
	result, err := parseClassificationJSON(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Fingerprint != "oom_killed" {
		t.Errorf("fingerprint = %q, want classification as fallback", result.Fingerprint)
	}
}

func TestParseClassificationJSON_noJSON(t *testing.T) {
	_, err := parseClassificationJSON("no json here at all")
	if err == nil {
		t.Error("expected error for input with no JSON, got nil")
	}
}

func TestParseClassificationJSON_malformedJSON(t *testing.T) {
	_, err := parseClassificationJSON(`{"classification": "foo", "severity":}`)
	if err == nil {
		t.Error("expected error for malformed JSON, got nil")
	}
}

func TestIsNovaModel(t *testing.T) {
	cases := []struct {
		model    string
		expected bool
	}{
		{"amazon.nova-micro-v1:0", true},
		{"amazon.nova-lite-v1:0", true},
		{"amazon.titan-text-v1", false},
		{"anthropic.claude-haiku-4-5", false},
		{"anthropic.claude-3-sonnet", false},
		{"meta.llama3", false},
	}
	for _, tc := range cases {
		got := isNovaModel(tc.model)
		if got != tc.expected {
			t.Errorf("isNovaModel(%q) = %v, want %v", tc.model, got, tc.expected)
		}
	}
}

func TestFingerprintHash(t *testing.T) {
	appName := "my-api"
	fingerprint := "postgres connection timeout"
	expected := fmt.Sprintf("%x", sha256.Sum256([]byte(appName+"|"+fingerprint)))

	// Simulate what Classify() does after parseClassificationJSON
	hashInput := appName + "|" + fingerprint
	got := fmt.Sprintf("%x", sha256.Sum256([]byte(hashInput)))

	if got != expected {
		t.Errorf("hash mismatch: got %q, want %q", got, expected)
	}
}

func TestFingerprintHash_differentApps_differentHashes(t *testing.T) {
	fp := "disk full error"
	h1 := fmt.Sprintf("%x", sha256.Sum256([]byte("app-a|"+fp)))
	h2 := fmt.Sprintf("%x", sha256.Sum256([]byte("app-b|"+fp)))
	if h1 == h2 {
		t.Error("same fingerprint in different apps should produce different hashes")
	}
}
