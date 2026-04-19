package store

import (
	"context"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUpsert_newTicket(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ticket := &Ticket{
		App:             "my-api",
		FingerprintHash: "abc123",
		FingerprintText: "postgres timeout",
		Classification:  "database_connection_timeout",
		Severity:        "critical",
		Component:       "payments",
		RawLog:          "ERROR: connection refused",
		Status:          "open",
	}

	result, err := s.Upsert(ctx, ticket)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if !result.IsNew {
		t.Error("first upsert should be IsNew=true")
	}
	if result.Ticket.ID == 0 {
		t.Error("expected non-zero ID after insert")
	}
	if result.Ticket.OccurrenceCount != 1 {
		t.Errorf("occurrence_count = %d, want 1", result.Ticket.OccurrenceCount)
	}
}

func TestUpsert_duplicateIncrementsCount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ticket := &Ticket{
		App:             "my-api",
		FingerprintHash: "abc123",
		FingerprintText: "postgres timeout",
		Classification:  "database_connection_timeout",
		Severity:        "critical",
		Component:       "payments",
		RawLog:          "ERROR: connection refused",
		Status:          "open",
	}

	r1, err := s.Upsert(ctx, ticket)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	r2, err := s.Upsert(ctx, ticket)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	if r2.IsNew {
		t.Error("second upsert should be IsNew=false")
	}
	if r2.Ticket.ID != r1.Ticket.ID {
		t.Errorf("ticket ID changed: got %d, want %d", r2.Ticket.ID, r1.Ticket.ID)
	}
	if r2.Ticket.OccurrenceCount != 2 {
		t.Errorf("occurrence_count = %d, want 2", r2.Ticket.OccurrenceCount)
	}
}

func TestUpsert_differentApps_differentTickets(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	base := &Ticket{
		FingerprintHash: "samehash",
		FingerprintText: "same fingerprint",
		Classification:  "oom_killed",
		Severity:        "high",
		Component:       "worker",
		RawLog:          "OOM killer",
		Status:          "open",
	}

	t1 := *base
	t1.App = "app-a"
	t2 := *base
	t2.App = "app-b"

	r1, _ := s.Upsert(ctx, &t1)
	r2, _ := s.Upsert(ctx, &t2)

	if !r1.IsNew || !r2.IsNew {
		t.Error("same fingerprint hash in different apps should create two separate tickets")
	}
	if r1.Ticket.ID == r2.Ticket.ID {
		t.Error("expected different IDs for different apps")
	}
}

func TestUpsert_multipleOccurrences(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ticket := &Ticket{
		App:             "svc",
		FingerprintHash: "fp1",
		FingerprintText: "disk full",
		Classification:  "disk_full",
		Severity:        "critical",
		Component:       "storage",
		RawLog:          "No space left on device",
		Status:          "open",
	}

	for i := 1; i <= 5; i++ {
		r, err := s.Upsert(ctx, ticket)
		if err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
		if r.Ticket.OccurrenceCount != i {
			t.Errorf("after upsert %d: occurrence_count = %d, want %d", i, r.Ticket.OccurrenceCount, i)
		}
	}
}

func TestListActiveTickets(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, err := s.Upsert(ctx, &Ticket{
			App:             "app",
			FingerprintHash: string(rune('a' + i)),
			FingerprintText: "fp",
			Classification:  "error",
			Severity:        "high",
			Component:       "svc",
			RawLog:          "err",
			Status:          "open",
		})
		if err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}

	tickets, err := s.ListActiveTickets(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tickets) != 3 {
		t.Errorf("got %d tickets, want 3", len(tickets))
	}
}

func TestListActiveTickets_excludesDone(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	r, err := s.Upsert(ctx, &Ticket{
		App:             "app",
		FingerprintHash: "h1",
		FingerprintText: "fp",
		Classification:  "error",
		Severity:        "low",
		Component:       "svc",
		RawLog:          "err",
		Status:          "open",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := s.UpdateStatus(ctx, r.Ticket.ID, StatusDone); err != nil {
		t.Fatalf("update status: %v", err)
	}

	tickets, err := s.ListActiveTickets(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tickets) != 0 {
		t.Errorf("expected 0 active tickets after done, got %d", len(tickets))
	}
}

func TestListActiveTickets_count(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	tickets, err := s.ListActiveTickets(ctx)
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if len(tickets) != 0 {
		t.Errorf("empty store count = %d, want 0", len(tickets))
	}

	s.Upsert(ctx, &Ticket{App: "a", FingerprintHash: "h1", FingerprintText: "f", Classification: "e", Severity: "low", Component: "c", RawLog: "r", Status: "open"})
	s.Upsert(ctx, &Ticket{App: "a", FingerprintHash: "h2", FingerprintText: "f", Classification: "e", Severity: "low", Component: "c", RawLog: "r", Status: "open"})

	tickets, err = s.ListActiveTickets(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tickets) != 2 {
		t.Errorf("count = %d, want 2", len(tickets))
	}
}

func TestUpdateSQSMessageID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	r, err := s.Upsert(ctx, &Ticket{
		App:             "app",
		FingerprintHash: "h1",
		FingerprintText: "fp",
		Classification:  "error",
		Severity:        "medium",
		Component:       "svc",
		RawLog:          "err",
		Status:          "open",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := s.UpdateSQSMessageID(ctx, r.Ticket.ID, "msg-abc-123"); err != nil {
		t.Fatalf("update sqs message id: %v", err)
	}

	tickets, _ := s.ListActiveTickets(ctx)
	if len(tickets) == 0 {
		t.Fatal("expected ticket in list")
	}
	if tickets[0].SQSMessageID != "msg-abc-123" {
		t.Errorf("sqs_message_id = %q, want %q", tickets[0].SQSMessageID, "msg-abc-123")
	}
}

func TestUpsert_preservesFirstSeen(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	firstSeen := time.Now().Add(-1 * time.Hour).UTC().Truncate(time.Second)
	ticket := &Ticket{
		App:             "app",
		FingerprintHash: "h1",
		FingerprintText: "fp",
		Classification:  "error",
		Severity:        "low",
		Component:       "svc",
		RawLog:          "err",
		Status:          "open",
		FirstSeen:       firstSeen,
	}

	r1, _ := s.Upsert(ctx, ticket)
	r2, _ := s.Upsert(ctx, ticket)

	if !r2.Ticket.FirstSeen.Equal(r1.Ticket.FirstSeen) {
		t.Errorf("first_seen changed on update: %v → %v", r1.Ticket.FirstSeen, r2.Ticket.FirstSeen)
	}
}
