package worker

import (
	"encoding/json"
	"fmt"
	"time"
)

type Ticket struct {
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

func ParseTicket(body string) (*Ticket, error) {
	var t Ticket
	if err := json.Unmarshal([]byte(body), &t); err != nil {
		return nil, fmt.Errorf("parsing ticket JSON: %w", err)
	}
	return &t, nil
}
