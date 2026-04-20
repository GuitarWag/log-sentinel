package poller

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/log-sentinel/sentinel/internal/classifier"
	"github.com/log-sentinel/sentinel/internal/config"
	"github.com/log-sentinel/sentinel/internal/queue"
	"github.com/log-sentinel/sentinel/internal/sources"
	"github.com/log-sentinel/sentinel/internal/store"
)

// --- fakes ---

type fakeSource struct {
	entries []sources.LogEntry
	err     error
}

func (f *fakeSource) FetchSince(_ context.Context, _ time.Time) ([]sources.LogEntry, error) {
	return f.entries, f.err
}

func (f *fakeSource) AppName() string { return "test-app" }

type fakeClassifier struct {
	result *classifier.ClassificationResult
	err    error
	calls  int
}

func (f *fakeClassifier) Classify(_ context.Context, _, _ string) (*classifier.ClassificationResult, error) {
	f.calls++
	return f.result, f.err
}

type fakePublisher struct {
	published []queue.TicketMessage
	msgID     string
	err       error
}

func (f *fakePublisher) Publish(_ context.Context, msg queue.TicketMessage) (string, error) {
	f.published = append(f.published, msg)
	if f.msgID == "" {
		f.msgID = "fake-msg-id"
	}
	return f.msgID, f.err
}

type fakeStore struct {
	tickets       map[string]*store.Ticket
	upsertResults map[string]*store.UpsertResult
	statusUpdates map[int64]string
	sqsUpdates    map[int64]string
	upsertErr     error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		tickets:       make(map[string]*store.Ticket),
		upsertResults: make(map[string]*store.UpsertResult),
		statusUpdates: make(map[int64]string),
		sqsUpdates:    make(map[int64]string),
	}
}

func (f *fakeStore) Upsert(_ context.Context, t *store.Ticket) (*store.UpsertResult, error) {
	if f.upsertErr != nil {
		return nil, f.upsertErr
	}
	key := t.App + "|" + t.FingerprintHash
	existing, ok := f.tickets[key]
	if !ok {
		t.ID = int64(len(f.tickets) + 1)
		t.OccurrenceCount = 1
		cp := *t
		f.tickets[key] = &cp
		return &store.UpsertResult{IsNew: true, Ticket: &cp}, nil
	}
	existing.OccurrenceCount++
	existing.LastSeen = t.LastSeen
	cp := *existing
	return &store.UpsertResult{IsNew: false, Ticket: &cp}, nil
}

func (f *fakeStore) UpdateSQSMessageID(_ context.Context, id int64, msgID string) error {
	f.sqsUpdates[id] = msgID
	return nil
}

func (f *fakeStore) UpdateStatus(_ context.Context, id int64, status string) error {
	f.statusUpdates[id] = status
	return nil
}

// --- helpers ---

func makeChannels() (Channels, chan LogMsg, chan TicketMsg, chan AppStatusMsg) {
	logCh := make(chan LogMsg, 100)
	ticketCh := make(chan TicketMsg, 100)
	statusCh := make(chan AppStatusMsg, 100)
	return Channels{LogCh: logCh, TicketCh: ticketCh, StatusCh: statusCh}, logCh, ticketCh, statusCh
}

func makeApp(name string) config.AppConfig {
	return config.AppConfig{
		Name:         name,
		Source:       "aws",
		PollInterval: config.Duration{Duration: 30 * time.Second},
	}
}

func classificationResult(fp string) *classifier.ClassificationResult {
	return &classifier.ClassificationResult{
		Classification:  "database_connection_timeout",
		Severity:        "critical",
		Component:       "payments",
		Fingerprint:     fp,
		FingerprintHash: "hash-" + fp,
	}
}

// --- tests ---

func TestPoll_newErrorLog_createsTicketAndPublishes(t *testing.T) {
	src := &fakeSource{entries: []sources.LogEntry{
		{AppName: "test-app", Timestamp: time.Now(), Severity: "ERROR", Message: "db timeout", RawLine: "ERROR: db timeout"},
	}}
	cls := &fakeClassifier{result: classificationResult("postgres-timeout")}
	pub := &fakePublisher{}
	db := newFakeStore()
	channels, _, ticketCh, _ := makeChannels()

	p := New(makeApp("test-app"), src, cls, pub, db, channels, &atomic.Bool{})
	p.poll(context.Background())

	if len(pub.published) != 1 {
		t.Errorf("SQS publish calls = %d, want 1", len(pub.published))
	}
	if len(ticketCh) != 1 {
		t.Errorf("ticket channel messages = %d, want 1", len(ticketCh))
	}
	msg := <-ticketCh
	if !msg.IsNew {
		t.Error("expected IsNew=true for first occurrence")
	}
}

func TestPoll_duplicateLog_doesNotRepublish(t *testing.T) {
	entry := sources.LogEntry{AppName: "test-app", Timestamp: time.Now(), Severity: "ERROR", RawLine: "ERROR: db timeout"}
	src := &fakeSource{entries: []sources.LogEntry{entry}}
	cls := &fakeClassifier{result: classificationResult("postgres-timeout")}
	pub := &fakePublisher{}
	db := newFakeStore()
	channels, _, _, _ := makeChannels()

	p := New(makeApp("test-app"), src, cls, pub, db, channels, &atomic.Bool{})
	p.poll(context.Background())
	p.poll(context.Background())

	if len(pub.published) != 1 {
		t.Errorf("SQS publish calls = %d, want 1 (second poll is a duplicate)", len(pub.published))
	}
}

func TestPoll_multipleDistinctErrors_createsMultipleTickets(t *testing.T) {
	src := &fakeSource{entries: []sources.LogEntry{
		{RawLine: "ERROR: db timeout"},
		{RawLine: "ERROR: oom killed"},
	}}
	fps := []string{"postgres-timeout", "oom-killed"}
	cls := &fakeClassifier{}

	pub := &fakePublisher{}
	db := newFakeStore()
	channels, _, ticketCh, _ := makeChannels()

	callCount := 0
	p := New(makeApp("test-app"), src, &multiClassifier{fps: fps, count: &callCount}, pub, db, channels, &atomic.Bool{})
	p.poll(context.Background())

	if len(pub.published) != 2 {
		t.Errorf("SQS publish calls = %d, want 2", len(pub.published))
	}
	if len(ticketCh) != 2 {
		t.Errorf("ticket channel messages = %d, want 2", len(ticketCh))
	}
	_ = cls
}

// multiClassifier returns a different fingerprint for each call.
type multiClassifier struct {
	fps   []string
	count *int
}

func (m *multiClassifier) Classify(_ context.Context, _, _ string) (*classifier.ClassificationResult, error) {
	i := *m.count
	if i >= len(m.fps) {
		i = len(m.fps) - 1
	}
	*m.count++
	return classificationResult(m.fps[i]), nil
}

func TestPoll_classifierError_skipsTicket(t *testing.T) {
	src := &fakeSource{entries: []sources.LogEntry{
		{RawLine: "ERROR: something"},
	}}
	cls := &fakeClassifier{err: errors.New("bedrock unavailable")}
	pub := &fakePublisher{}
	db := newFakeStore()
	channels, _, ticketCh, _ := makeChannels()

	p := New(makeApp("test-app"), src, cls, pub, db, channels, &atomic.Bool{})
	p.poll(context.Background())

	if len(pub.published) != 0 {
		t.Error("should not publish when classifier fails")
	}
	if len(ticketCh) != 0 {
		t.Error("should not emit ticket when classifier fails")
	}
}

func TestPoll_sqsError_marksTicketPending(t *testing.T) {
	src := &fakeSource{entries: []sources.LogEntry{
		{RawLine: "ERROR: db timeout"},
	}}
	cls := &fakeClassifier{result: classificationResult("postgres-timeout")}
	pub := &fakePublisher{err: errors.New("sqs unavailable")}
	db := newFakeStore()
	channels, _, _, _ := makeChannels()

	p := New(makeApp("test-app"), src, cls, pub, db, channels, &atomic.Bool{})
	p.poll(context.Background())

	// Ticket should still be in the store
	if len(db.tickets) != 1 {
		t.Errorf("store tickets = %d, want 1", len(db.tickets))
	}
	// Status should be set to "pending"
	for id, status := range db.statusUpdates {
		if status != "pending" {
			t.Errorf("ticket %d status = %q, want %q", id, status, "pending")
		}
	}
}

func TestPoll_fetchError_sendsErrorStatus(t *testing.T) {
	src := &fakeSource{err: errors.New("network error")}
	cls := &fakeClassifier{}
	pub := &fakePublisher{}
	db := newFakeStore()
	channels, _, _, statusCh := makeChannels()

	p := New(makeApp("test-app"), src, cls, pub, db, channels, &atomic.Bool{})
	p.poll(context.Background())

	if cls.calls != 0 {
		t.Error("classifier should not be called when fetch fails")
	}

	// Should have received an error status
	var gotError bool
	for len(statusCh) > 0 {
		s := <-statusCh
		if s.Status == "error" {
			gotError = true
		}
	}
	if !gotError {
		t.Error("expected error status message when fetch fails")
	}
}

func TestPoll_noLogs_publishesNothing(t *testing.T) {
	src := &fakeSource{entries: nil}
	cls := &fakeClassifier{}
	pub := &fakePublisher{}
	db := newFakeStore()
	channels, _, ticketCh, _ := makeChannels()

	p := New(makeApp("test-app"), src, cls, pub, db, channels, &atomic.Bool{})
	p.poll(context.Background())

	if cls.calls != 0 {
		t.Error("classifier should not be called with no logs")
	}
	if len(ticketCh) != 0 {
		t.Error("no ticket messages expected with no logs")
	}
}

func TestPoll_logsAreSentToLogChannel(t *testing.T) {
	src := &fakeSource{entries: []sources.LogEntry{
		{RawLine: "ERROR: one"},
		{RawLine: "ERROR: two"},
	}}
	fp := 0
	fps := []string{"fp1", "fp2"}
	cls := &multiClassifier{fps: fps, count: &fp}
	pub := &fakePublisher{}
	db := newFakeStore()
	channels, logCh, _, _ := makeChannels()

	p := New(makeApp("test-app"), src, cls, pub, db, channels, &atomic.Bool{})
	p.poll(context.Background())

	if len(logCh) != 2 {
		t.Errorf("log channel messages = %d, want 2", len(logCh))
	}
}
