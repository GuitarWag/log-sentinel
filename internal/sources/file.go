package sources

import (
	"bufio"
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"
)

type FileSource struct {
	appName string
	path    string
	lines   []string
	rng     *rand.Rand
}

func NewFileSource(appName, path string) (*FileSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening error log %q: %w", path, err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading error log %q: %w", path, err)
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("error log %q has no entries", path)
	}

	return &FileSource{
		appName: appName,
		path:    path,
		lines:   lines,
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}, nil
}

func (s *FileSource) AppName() string {
	return s.appName
}

func (s *FileSource) FetchSince(_ context.Context, _ time.Time) ([]LogEntry, error) {
	count := s.rng.Intn(2) + 1
	entries := make([]LogEntry, 0, count)

	for i := 0; i < count; i++ {
		raw := s.lines[s.rng.Intn(len(s.lines))]
		severity, message := parseSeverityPrefix(raw)
		ts := time.Now().Add(-time.Duration(s.rng.Intn(60)) * time.Second)

		entries = append(entries, LogEntry{
			AppName:   s.appName,
			Timestamp: ts,
			Severity:  severity,
			Message:   message,
			RawLine:   fmt.Sprintf("[%s] %s %s", ts.Format(time.RFC3339), severity, message),
		})
	}

	return entries, nil
}

// parseSeverityPrefix splits "ERROR some message" into ("ERROR", "some message").
// Lines without a recognised prefix default to ERROR.
func parseSeverityPrefix(line string) (severity, message string) {
	for _, sev := range []string{"CRITICAL", "ERROR", "WARN", "WARNING", "INFO", "DEBUG"} {
		if strings.HasPrefix(strings.ToUpper(line), sev+" ") {
			return sev, line[len(sev)+1:]
		}
	}
	return "ERROR", line
}
