package worker

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/log-sentinel/sentinel/internal/config"
)

// --- fakes ---

type fakeSQS struct {
	messages  []sqstypes.Message
	deleted   []string
	visChange []string
	callCount int
	err       error
}

func (f *fakeSQS) ReceiveMessage(_ context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	f.callCount++
	if f.err != nil {
		return nil, f.err
	}
	if len(f.messages) == 0 {
		return &sqs.ReceiveMessageOutput{}, nil
	}
	// Return one message at a time, pop it
	msg := f.messages[0]
	f.messages = f.messages[1:]
	return &sqs.ReceiveMessageOutput{Messages: []sqstypes.Message{msg}}, nil
}

func (f *fakeSQS) DeleteMessage(_ context.Context, params *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	f.deleted = append(f.deleted, aws.ToString(params.ReceiptHandle))
	return &sqs.DeleteMessageOutput{}, nil
}

func (f *fakeSQS) ChangeMessageVisibility(_ context.Context, params *sqs.ChangeMessageVisibilityInput, _ ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	f.visChange = append(f.visChange, aws.ToString(params.ReceiptHandle))
	return &sqs.ChangeMessageVisibilityOutput{}, nil
}

func makeTicketMsg(t *testing.T, ticket *Ticket) sqstypes.Message {
	t.Helper()
	body, _ := json.Marshal(ticket)
	return sqstypes.Message{
		Body:          aws.String(string(body)),
		ReceiptHandle: aws.String("rh-" + ticket.Classification),
		MessageAttributes: map[string]sqstypes.MessageAttributeValue{
			"AppName": {StringValue: aws.String(ticket.AppName)},
		},
	}
}

func sampleTicket(app string) *Ticket {
	return &Ticket{
		AppName:         app,
		Classification:  "database_connection_timeout",
		Severity:        "critical",
		Component:       "payments",
		FingerprintHash: "abc123",
		FingerprintText: "postgres connection timeout",
		RawLog:          "ERROR: connection refused",
		FirstSeen:       time.Now(),
	}
}

// --- action tests ---

func TestWebhookAction_success(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = make([]byte, r.ContentLength)
		r.Body.Read(received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	action := &WebhookAction{
		cfg:     config.ActionConfig{URL: srv.URL, Type: "webhook"},
		timeout: 5 * time.Second,
	}

	ticket := sampleTicket("my-api")
	if err := action.Run(context.Background(), ticket); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got Ticket
	if err := json.Unmarshal(received, &got); err != nil {
		t.Fatalf("webhook body not valid JSON: %v", err)
	}
	if got.AppName != ticket.AppName {
		t.Errorf("app_name = %q, want %q", got.AppName, ticket.AppName)
	}
}

func TestWebhookAction_non2xx_returnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	action := &WebhookAction{
		cfg:     config.ActionConfig{URL: srv.URL},
		timeout: 5 * time.Second,
	}

	if err := action.Run(context.Background(), sampleTicket("app")); err == nil {
		t.Error("expected error for HTTP 500, got nil")
	}
}

func TestWebhookAction_hmacSignature(t *testing.T) {
	var sigHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sigHeader = r.Header.Get("X-Signature-256")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	action := &WebhookAction{
		cfg:     config.ActionConfig{URL: srv.URL, Secret: "my-secret"},
		timeout: 5 * time.Second,
	}

	if err := action.Run(context.Background(), sampleTicket("app")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(sigHeader, "sha256=") {
		t.Errorf("X-Signature-256 = %q, expected sha256= prefix", sigHeader)
	}
}

func TestCLIAgentAction_argMode(t *testing.T) {
	action := &CLIAgentAction{
		cfg: config.ActionConfig{
			Command:        "echo",
			Args:           []string{},
			PromptTemplate: "investigate: {{.Classification}}",
		},
		timeout:    5 * time.Second,
		promptMode: "arg",
	}

	if err := action.Run(context.Background(), sampleTicket("app")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCLIAgentAction_stdinMode(t *testing.T) {
	action := &CLIAgentAction{
		cfg: config.ActionConfig{
			Command:        "cat",
			Args:           []string{},
			PromptTemplate: "severity: {{.Severity}}",
		},
		timeout:    5 * time.Second,
		promptMode: "stdin",
	}

	if err := action.Run(context.Background(), sampleTicket("app")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCLIAgentAction_nonZeroExit_returnsError(t *testing.T) {
	action := &CLIAgentAction{
		cfg:        config.ActionConfig{Command: "false"},
		timeout:    5 * time.Second,
		promptMode: "arg",
	}

	if err := action.Run(context.Background(), sampleTicket("app")); err == nil {
		t.Error("expected error for non-zero exit, got nil")
	}
}

func TestCLIAgentAction_promptTemplateRendered(t *testing.T) {
	var got string
	// Use a shell one-liner to capture the arg
	action := &CLIAgentAction{
		cfg: config.ActionConfig{
			Command:        "sh",
			Args:           []string{"-c", "echo \"$1\" > /dev/null", "--"},
			PromptTemplate: "fix {{.Classification}} in {{.Component}}",
		},
		timeout:    5 * time.Second,
		promptMode: "arg",
	}

	ticket := sampleTicket("app")
	if err := action.Run(context.Background(), ticket); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the template renders correctly (independent of the subprocess)
	rendered, err := action.renderPrompt(ticket)
	if err != nil {
		t.Fatalf("renderPrompt error: %v", err)
	}
	got = rendered
	if !strings.Contains(got, ticket.Classification) {
		t.Errorf("rendered prompt missing classification %q: %q", ticket.Classification, got)
	}
	if !strings.Contains(got, ticket.Component) {
		t.Errorf("rendered prompt missing component %q: %q", ticket.Component, got)
	}
}

func TestBuildAction_webhook(t *testing.T) {
	a, err := BuildAction(config.ActionConfig{Type: "webhook", URL: "http://example.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := a.(*WebhookAction); !ok {
		t.Errorf("expected *WebhookAction, got %T", a)
	}
}

func TestBuildAction_cliAgent(t *testing.T) {
	a, err := BuildAction(config.ActionConfig{Type: "cli_agent", Command: "echo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := a.(*CLIAgentAction); !ok {
		t.Errorf("expected *CLIAgentAction, got %T", a)
	}
}

func TestBuildAction_unknownType(t *testing.T) {
	_, err := BuildAction(config.ActionConfig{Type: "send_fax"})
	if err == nil {
		t.Error("expected error for unknown action type")
	}
}

func TestBuildAction_webhookMissingURL(t *testing.T) {
	_, err := BuildAction(config.ActionConfig{Type: "webhook"})
	if err == nil {
		t.Error("expected error for webhook without url")
	}
}

// --- worker process tests ---

func TestWorker_processesTicketAndAcks(t *testing.T) {
	var actionCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actionCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sqsFake := &fakeSQS{
		messages: []sqstypes.Message{makeTicketMsg(t, sampleTicket("my-app"))},
	}

	w, err := New(
		config.WorkerConfig{
			App:         "my-app",
			Concurrency: 1,
			Actions: []config.ActionConfig{
				{Type: "webhook", URL: srv.URL},
			},
		},
		"http://queue",
		sqsFake,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("building worker: %v", err)
	}

	// Run one process cycle directly
	ctx := context.Background()
	msg := sqsFake.messages[0]
	sqsFake.messages = sqsFake.messages[1:] // consumed manually
	w.process(ctx, slog.Default(), msg)

	if !actionCalled {
		t.Error("action was not called")
	}
	if len(sqsFake.deleted) != 1 {
		t.Errorf("message deleted = %d, want 1", len(sqsFake.deleted))
	}
}

func TestWorker_actionFailure_doesNotAck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	sqsFake := &fakeSQS{}

	w, err := New(
		config.WorkerConfig{
			App: "my-app",
			Actions: []config.ActionConfig{
				{Type: "webhook", URL: srv.URL},
			},
		},
		"http://queue",
		sqsFake,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("building worker: %v", err)
	}

	ctx := context.Background()
	w.process(ctx, slog.Default(), makeTicketMsg(t, sampleTicket("my-app")))

	if len(sqsFake.deleted) != 0 {
		t.Error("message should not be deleted when action fails")
	}
	if len(sqsFake.visChange) != 1 {
		t.Errorf("visibility change = %d, want 1 (retry backoff)", len(sqsFake.visChange))
	}
}

func TestWorker_malformedMessage_discarded(t *testing.T) {
	sqsFake := &fakeSQS{}

	w, _ := New(
		config.WorkerConfig{App: "app", Actions: []config.ActionConfig{{Type: "webhook", URL: "http://x"}}},
		"http://queue",
		sqsFake,
		nil,
		nil,
	)

	ctx := context.Background()
	w.process(ctx, slog.Default(), sqstypes.Message{
		Body:          aws.String("not json at all"),
		ReceiptHandle: aws.String("rh-bad"),
	})

	if len(sqsFake.deleted) != 1 {
		t.Error("malformed message should be deleted to avoid infinite redelivery")
	}
}

func TestParseTicket(t *testing.T) {
	ticket := sampleTicket("app")
	body, _ := json.Marshal(ticket)

	got, err := ParseTicket(string(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.AppName != ticket.AppName {
		t.Errorf("app_name = %q, want %q", got.AppName, ticket.AppName)
	}
	if got.Classification != ticket.Classification {
		t.Errorf("classification = %q, want %q", got.Classification, ticket.Classification)
	}
}

func TestParseTicket_invalid(t *testing.T) {
	_, err := ParseTicket("{bad json")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}
