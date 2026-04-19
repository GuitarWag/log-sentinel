package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

type MockPublisher struct {
	path    string
	mu      sync.Mutex
	counter int
}

func NewMockPublisher(path string) *MockPublisher {
	return &MockPublisher{path: path}
}

type mockRecord struct {
	MockMessageID string    `json:"mock_message_id"`
	PublishedAt   time.Time `json:"published_at"`
	TicketMessage
}

func (m *MockPublisher) Publish(_ context.Context, msg TicketMessage) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.counter++
	msgID := fmt.Sprintf("mock-msg-%d-%d", time.Now().UnixMilli(), m.counter)

	record := mockRecord{
		MockMessageID: msgID,
		PublishedAt:   time.Now().UTC(),
		TicketMessage: msg,
	}

	line, err := json.Marshal(record)
	if err != nil {
		return "", fmt.Errorf("marshaling mock record: %w", err)
	}

	f, err := os.OpenFile(m.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", fmt.Errorf("opening mock queue file %q: %w", m.path, err)
	}
	defer f.Close()

	if _, err := fmt.Fprintf(f, "%s\n", line); err != nil {
		return "", fmt.Errorf("writing to mock queue file: %w", err)
	}

	return msgID, nil
}
