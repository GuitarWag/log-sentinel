package tui

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/log-sentinel/sentinel/internal/poller"
	"github.com/log-sentinel/sentinel/internal/queue"
	"github.com/log-sentinel/sentinel/internal/sources"
	"github.com/log-sentinel/sentinel/internal/store"
	"github.com/log-sentinel/sentinel/internal/worker"
)

const maxLogLines = 200
const maxWorkerEvents = 200
const maxSentinelLines = 500

type tickMsg struct{ poller.TicketMsg }
type logMsg struct{ poller.LogMsg }
type statusMsg struct{ poller.AppStatusMsg }
type workerEvMsg struct{ worker.Event }
type sentinelLogMsg struct{ SentinelLogRecord }
type sqsStatsMsg struct{ queue.QueueStats }

type Model struct {
	width  int
	height int

	appStatuses map[string]*appStatus
	appOrder    []string

	logLines     []sources.LogEntry
	tickets      []*store.Ticket
	ticketsById  map[int64]*store.Ticket
	workerEvents []worker.Event

	workerByTicket map[int64][]worker.Event

	activeTab int

	ticketCursor int
	ticketOffset int
	showDetail   bool

	ticketStatusFilter string
	ticketAppFilter    string
	ticketSevFilter    string
	ticketStatusIdx    int
	ticketAppIdx       int
	ticketSevIdx       int

	logAppFilter string
	logAppIdx    int

	workerAppFilter string
	workerAppIdx    int

	logVP    viewport.Model
	workerVP viewport.Model
	detailVP viewport.Model
	sentinelVP viewport.Model

	sentinelLines       []SentinelLogRecord
	sentinelLevelFilter string
	sentinelLevelIdx    int
	sentinelSearch      string

	logCh         <-chan poller.LogMsg
	ticketCh      <-chan poller.TicketMsg
	statusCh      <-chan poller.AppStatusMsg
	workerEvCh    <-chan worker.Event
	sentinelLogCh <-chan SentinelLogRecord
	sqsStatsCh    <-chan queue.QueueStats

	pollersPaused *atomic.Bool
	workersPaused *atomic.Bool
	sqsStats      queue.QueueStats

	ctx    context.Context
	cancel context.CancelFunc
}

type appStatus struct {
	Name     string
	Status   string
	ErrorMsg string
}

type Channels struct {
	LogCh         <-chan poller.LogMsg
	TicketCh      <-chan poller.TicketMsg
	StatusCh      <-chan poller.AppStatusMsg
	WorkerEvCh    <-chan worker.Event
	SentinelLogCh <-chan SentinelLogRecord
	SQSStatsCh    <-chan queue.QueueStats
	PollersPaused *atomic.Bool
	WorkersPaused *atomic.Bool
}

func New(ctx context.Context, cancel context.CancelFunc, appNames []string, ch Channels) Model {
	lv := viewport.New(80, 20)
	wv := viewport.New(80, 20)
	dv := viewport.New(80, 20)
	sv := viewport.New(80, 20)

	lv.Style = lipgloss.NewStyle()
	wv.Style = lipgloss.NewStyle()
	dv.Style = lipgloss.NewStyle()
	sv.Style = lipgloss.NewStyle()

	lv.KeyMap = viewport.KeyMap{}
	wv.KeyMap = viewport.KeyMap{}
	dv.KeyMap = viewport.KeyMap{}
	sv.KeyMap = viewport.KeyMap{}

	statuses := make(map[string]*appStatus, len(appNames))
	for _, name := range appNames {
		statuses[name] = &appStatus{Name: name, Status: "idle"}
	}

	return Model{
		appStatuses:    statuses,
		appOrder:       appNames,
		ticketsById:    make(map[int64]*store.Ticket),
		workerByTicket: make(map[int64][]worker.Event),
		logVP:          lv,
		workerVP:       wv,
		detailVP:       dv,
		sentinelVP:     sv,
		logCh:          ch.LogCh,
		ticketCh:       ch.TicketCh,
		statusCh:       ch.StatusCh,
		workerEvCh:     ch.WorkerEvCh,
		sentinelLogCh:  ch.SentinelLogCh,
		sqsStatsCh:     ch.SQSStatsCh,
		pollersPaused:  ch.PollersPaused,
		workersPaused:  ch.WorkersPaused,
		ctx:            ctx,
		cancel:         cancel,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		waitForLog(m.ctx, m.logCh),
		waitForTicket(m.ctx, m.ticketCh),
		waitForStatus(m.ctx, m.statusCh),
		waitForWorkerEvent(m.ctx, m.workerEvCh),
		waitForSentinelLog(m.ctx, m.sentinelLogCh),
		waitForSQSStats(m.ctx, m.sqsStatsCh),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.recalcSizes()

	case tea.KeyMsg:
		key := msg.String()
		switch key {
		case "q", "ctrl+c":
			m.cancel()
			return m, tea.Quit
		case "1":
			m.activeTab = 0
		case "2":
			m.activeTab = 1
		case "3":
			m.activeTab = 2
		case "4":
			m.activeTab = 3
		case "5":
			m.activeTab = 4
		case "tab":
			m.activeTab = (m.activeTab + 1) % 5
		case "shift+tab":
			m.activeTab = (m.activeTab + 4) % 5
		case "p":
			if m.activeTab != 4 && m.pollersPaused != nil {
				m.pollersPaused.Store(!m.pollersPaused.Load())
			}
		case "w":
			if m.activeTab != 4 && m.workersPaused != nil {
				m.workersPaused.Store(!m.workersPaused.Load())
			}
		default:
			switch m.activeTab {
			case 1:
				m.handleLogsKey(key)
			case 2:
				m.handleTicketsKey(key)
			case 3:
				m.handleWorkersKey(key)
			case 4:
				m.handleSentinelKey(key)
			}
		}

	case logMsg:
		m.addLogEntry(msg.Entry)
		m.refreshLogVP()
		cmds = append(cmds, waitForLog(m.ctx, m.logCh))

	case tickMsg:
		m.updateTicket(msg.Ticket, msg.IsNew)
		cmds = append(cmds, waitForTicket(m.ctx, m.ticketCh))

	case statusMsg:
		if s, ok := m.appStatuses[msg.AppName]; ok {
			s.Status = msg.Status
			s.ErrorMsg = msg.ErrorMsg
		}
		cmds = append(cmds, waitForStatus(m.ctx, m.statusCh))

	case workerEvMsg:
		e := msg.Event
		m.addWorkerEvent(e)

		if e.TicketID > 0 {
			if t, ok := m.ticketsById[e.TicketID]; ok {
				switch e.Status {
				case "processing":
					t.Status = store.StatusInProgress
				case "done":
					t.Status = store.StatusDone
				case "failed":
					t.Status = store.StatusFailed
				}
			}
			m.addWorkerEventForTicket(e)
		}

		m.refreshWorkerVP()
		if m.showDetail && m.activeTab == 2 {
			filtered := m.filteredTickets()
			if m.ticketCursor < len(filtered) {
				t := filtered[m.ticketCursor]
				if t.ID == e.TicketID {
					m.detailVP.SetContent(m.renderDetailContent(t))
				}
			}
		}
		cmds = append(cmds, waitForWorkerEvent(m.ctx, m.workerEvCh))

	case sentinelLogMsg:
		m.addSentinelLine(msg.SentinelLogRecord)
		m.refreshSentinelVP()
		cmds = append(cmds, waitForSentinelLog(m.ctx, m.sentinelLogCh))

	case sqsStatsMsg:
		m.sqsStats = msg.QueueStats
		cmds = append(cmds, waitForSQSStats(m.ctx, m.sqsStatsCh))
	}

	if _, isKey := msg.(tea.KeyMsg); !isKey {
		var cmd tea.Cmd
		m.logVP, cmd = m.logVP.Update(msg)
		cmds = append(cmds, cmd)
		m.workerVP, cmd = m.workerVP.Update(msg)
		cmds = append(cmds, cmd)
		m.detailVP, cmd = m.detailVP.Update(msg)
		cmds = append(cmds, cmd)
		m.sentinelVP, cmd = m.sentinelVP.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m Model) View() string {
	if m.width == 0 {
		return "Initializing..."
	}
	return RenderLayout(m)
}

func (m *Model) handleLogsKey(key string) {
	switch key {
	case "up", "k":
		m.logVP.LineUp(1)
	case "down", "j":
		m.logVP.LineDown(1)
	case "g":
		m.logVP.GotoTop()
	case "G":
		m.logVP.GotoBottom()
	case "a":
		m.cycleLogAppFilter()
		m.refreshLogVP()
	}
}

func (m *Model) handleTicketsKey(key string) {
	if m.showDetail {
		switch key {
		case "up", "k":
			m.detailVP.LineUp(1)
		case "down", "j":
			m.detailVP.LineDown(1)
		case "esc":
			m.showDetail = false
		}
		return
	}

	switch key {
	case "up", "k":
		if m.ticketCursor > 0 {
			m.ticketCursor--
			m.clampTicketScroll()
		}
	case "down", "j":
		filtered := m.filteredTickets()
		if m.ticketCursor < len(filtered)-1 {
			m.ticketCursor++
			m.clampTicketScroll()
		}
	case "enter":
		filtered := m.filteredTickets()
		if len(filtered) > 0 && m.ticketCursor < len(filtered) {
			t := filtered[m.ticketCursor]
			m.detailVP.SetContent(m.renderDetailContent(t))
			m.detailVP.GotoTop()
			m.showDetail = true
		}
	case "esc":
		m.showDetail = false
	case "f":
		m.cycleTicketStatusFilter()
		m.ticketCursor = 0
		m.ticketOffset = 0
	case "a":
		m.cycleTicketAppFilter()
		m.ticketCursor = 0
		m.ticketOffset = 0
	case "s":
		m.cycleTicketSevFilter()
		m.ticketCursor = 0
		m.ticketOffset = 0
	}
}

func (m *Model) handleWorkersKey(key string) {
	switch key {
	case "up", "k":
		m.workerVP.LineUp(1)
	case "down", "j":
		m.workerVP.LineDown(1)
	case "g":
		m.workerVP.GotoTop()
	case "G":
		m.workerVP.GotoBottom()
	case "a":
		m.cycleWorkerAppFilter()
		m.refreshWorkerVP()
	}
}

func (m *Model) filteredTickets() []*store.Ticket {
	result := make([]*store.Ticket, 0, len(m.tickets))
	for _, t := range m.tickets {
		if m.ticketStatusFilter != "" && t.Status != m.ticketStatusFilter {
			continue
		}
		if m.ticketAppFilter != "" && t.App != m.ticketAppFilter {
			continue
		}
		if m.ticketSevFilter != "" {
			normSev := normalizeSeverity(t.Severity)
			if normSev != m.ticketSevFilter {
				continue
			}
		}
		result = append(result, t)
	}
	return result
}

func (m *Model) filteredLogs() []sources.LogEntry {
	if m.logAppFilter == "" {
		return m.logLines
	}
	result := make([]sources.LogEntry, 0, len(m.logLines))
	for _, e := range m.logLines {
		if e.AppName == m.logAppFilter {
			result = append(result, e)
		}
	}
	return result
}

func (m *Model) filteredWorkerEvents() []worker.Event {
	if m.workerAppFilter == "" {
		return m.workerEvents
	}
	result := make([]worker.Event, 0, len(m.workerEvents))
	for _, e := range m.workerEvents {
		if e.App == m.workerAppFilter {
			result = append(result, e)
		}
	}
	return result
}

func (m *Model) ticketListVisibleItems() int {
	h := m.height - 7
	if h < 1 {
		return 1
	}
	n := h / 3
	if n < 1 {
		n = 1
	}
	return n
}

func (m *Model) clampTicketScroll() {
	visible := m.ticketListVisibleItems()
	if m.ticketCursor < m.ticketOffset {
		m.ticketOffset = m.ticketCursor
	}
	if m.ticketCursor >= m.ticketOffset+visible {
		m.ticketOffset = m.ticketCursor - visible + 1
	}
}

func (m *Model) addLogEntry(entry sources.LogEntry) {
	m.logLines = append(m.logLines, entry)
	if len(m.logLines) > maxLogLines {
		m.logLines = m.logLines[len(m.logLines)-maxLogLines:]
	}
}

func (m *Model) updateTicket(t *store.Ticket, isNew bool) {
	if t.ID == 0 {
		return
	}
	if isNew {
		m.ticketsById[t.ID] = t
		m.tickets = append([]*store.Ticket{t}, m.tickets...)
	} else {
		if existing, ok := m.ticketsById[t.ID]; ok {
			*existing = *t
		} else {
			m.ticketsById[t.ID] = t
			m.tickets = append([]*store.Ticket{t}, m.tickets...)
		}
	}
	if len(m.tickets) > 200 {
		for _, removed := range m.tickets[200:] {
			delete(m.ticketsById, removed.ID)
		}
		m.tickets = m.tickets[:200]
	}
}

func (m *Model) addWorkerEvent(e worker.Event) {
	m.workerEvents = append(m.workerEvents, e)
	if len(m.workerEvents) > maxWorkerEvents {
		m.workerEvents = m.workerEvents[len(m.workerEvents)-maxWorkerEvents:]
	}
}

const maxWorkerEventsPerTicket = 20

func (m *Model) addWorkerEventForTicket(e worker.Event) {
	events := m.workerByTicket[e.TicketID]
	events = append(events, e)
	if len(events) > maxWorkerEventsPerTicket {
		events = events[len(events)-maxWorkerEventsPerTicket:]
	}
	m.workerByTicket[e.TicketID] = events
}

func (m *Model) recalcSizes() {
	if m.width == 0 || m.height == 0 {
		return
	}
	vpH := m.height - 7
	vpW := m.width - 4
	if vpH < 1 {
		vpH = 1
	}
	if vpW < 1 {
		vpW = 1
	}
	m.logVP.Width = vpW
	m.logVP.Height = vpH
	m.workerVP.Width = vpW
	m.workerVP.Height = vpH
	m.sentinelVP.Width = vpW
	m.sentinelVP.Height = vpH
	m.detailVP.Width = vpW - 2
	m.detailVP.Height = vpH - 2
	if m.detailVP.Width < 1 {
		m.detailVP.Width = 1
	}
	if m.detailVP.Height < 1 {
		m.detailVP.Height = 1
	}
}

func (m *Model) refreshLogVP() {
	entries := m.filteredLogs()
	if len(entries) == 0 {
		m.logVP.SetContent(styleTimestamp.Render("Waiting for log entries..."))
		return
	}
	var sb strings.Builder
	for _, e := range entries {
		sb.WriteString(renderLogLine(e))
		sb.WriteString("\n")
	}
	m.logVP.SetContent(sb.String())
	m.logVP.GotoBottom()
}

func (m *Model) refreshWorkerVP() {
	events := m.filteredWorkerEvents()
	if len(events) == 0 {
		m.workerVP.SetContent(styleTimestamp.Render("No worker activity yet..."))
		return
	}
	var sb strings.Builder
	for _, e := range events {
		sb.WriteString(renderWorkerEventLine(e))
		sb.WriteString("\n")
	}
	m.workerVP.SetContent(sb.String())
	m.workerVP.GotoBottom()
}

func (m *Model) renderDetailContent(t *store.Ticket) string {
	var sb strings.Builder

	titleStatus := renderTicketStatus(t.Status)
	titleSev := renderSeverityBadge(t.Severity)
	sb.WriteString(styleAppName.Render("Ticket #") + styleCount.Render(itoa(t.ID)))
	sb.WriteString("  " + titleStatus + "  " + titleSev + "\n")
	sb.WriteString(strings.Repeat("─", 54) + "\n\n")

	sb.WriteString(styleTimestamp.Render("  App:             ") + t.App + "\n")
	sb.WriteString(styleTimestamp.Render("  Classification:  ") + t.Classification + "\n")
	sb.WriteString(styleTimestamp.Render("  Component:       ") + t.Component + "\n")
	sb.WriteString(styleTimestamp.Render("  Created:         ") + t.CreatedDate + "\n")

	firstSeen := ""
	if !t.FirstSeen.IsZero() {
		firstSeen = t.FirstSeen.Format("15:04:05")
	}
	sb.WriteString(styleTimestamp.Render("  First seen:      ") +
		firstSeen + styleCount.Render("  ×"+itoa(int64(t.OccurrenceCount))+" occurrences") + "\n")

	lastSeen := ""
	if !t.LastSeen.IsZero() {
		lastSeen = formatRelativeTime(t.LastSeen)
	}
	sb.WriteString(styleTimestamp.Render("  Last seen:       ") + lastSeen + "\n\n")

	sb.WriteString(stylePanelTitle.Render("  Fingerprint") + "\n")
	sb.WriteString("  " + t.FingerprintText + "\n\n")

	sb.WriteString(stylePanelTitle.Render("  Raw Log") + "\n")
	rawLog := t.RawLog
	if len(rawLog) > 200 {
		rawLog = rawLog[:200] + "…"
	}
	sb.WriteString("  " + rawLog + "\n\n")

	events := m.workerByTicket[t.ID]
	if len(events) > 0 {
		sb.WriteString(stylePanelTitle.Render("  Worker Activity") + "\n")
		for _, e := range events {
			ts := styleTimestamp.Render(e.Timestamp.Format("15:04:05"))
			var statusStr string
			switch e.Status {
			case "processing":
				statusStr = styleWorkerProcessing.Render("⟳ running")
			case "done":
				statusStr = styleWorkerDone.Render("✓ done")
			case "failed":
				statusStr = styleWorkerFailed.Render("✗ failed")
			}
			sb.WriteString("  " + ts + "  " + statusStr + "  " + e.ActionName + "\n")
		}

		var lastOutput string
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Status == "done" && events[i].Output != "" {
				lastOutput = events[i].Output
				break
			}
		}
		if lastOutput != "" {
			sb.WriteString("\n")
			sb.WriteString(stylePanelTitle.Render("  Agent Output") + "\n")
			for _, line := range strings.SplitAfter(lastOutput, "\n") {
				sb.WriteString("  " + styleWorkerDone.Render(line))
			}
			sb.WriteString("\n")
		}
	} else {
		sb.WriteString(styleTimestamp.Render("  No worker activity for this ticket yet.") + "\n")
	}

	return sb.String()
}

func (m *Model) cycleTicketStatusFilter() {
	options := []string{"", "open", "in_progress", "done", "failed"}
	m.ticketStatusIdx = (m.ticketStatusIdx + 1) % len(options)
	m.ticketStatusFilter = options[m.ticketStatusIdx]
}

func (m *Model) cycleTicketAppFilter() {
	options := make([]string, 0, 1+len(m.appOrder))
	options = append(options, "")
	options = append(options, m.appOrder...)
	m.ticketAppIdx = (m.ticketAppIdx + 1) % len(options)
	m.ticketAppFilter = options[m.ticketAppIdx]
}

func (m *Model) cycleTicketSevFilter() {
	options := []string{"", "critical", "high", "medium", "low"}
	m.ticketSevIdx = (m.ticketSevIdx + 1) % len(options)
	m.ticketSevFilter = options[m.ticketSevIdx]
}

func (m *Model) cycleLogAppFilter() {
	options := make([]string, 0, 1+len(m.appOrder))
	options = append(options, "")
	options = append(options, m.appOrder...)
	m.logAppIdx = (m.logAppIdx + 1) % len(options)
	m.logAppFilter = options[m.logAppIdx]
}

func (m *Model) cycleWorkerAppFilter() {
	options := make([]string, 0, 1+len(m.appOrder))
	options = append(options, "")
	options = append(options, m.appOrder...)
	m.workerAppIdx = (m.workerAppIdx + 1) % len(options)
	m.workerAppFilter = options[m.workerAppIdx]
}

func normalizeSeverity(s string) string {
	switch strings.ToUpper(s) {
	case "CRITICAL", "FATAL":
		return "critical"
	case "HIGH", "ERROR":
		return "high"
	case "MEDIUM", "WARNING", "WARN":
		return "medium"
	default:
		return "low"
	}
}

func (m Model) statsOpen() int {
	n := 0
	for _, t := range m.tickets {
		if t.Status == store.StatusOpen {
			n++
		}
	}
	return n
}

func (m Model) statsActive() int {
	n := 0
	for _, t := range m.tickets {
		if t.Status == store.StatusInProgress {
			n++
		}
	}
	return n
}

func (m Model) statsDone() int {
	n := 0
	for _, t := range m.tickets {
		if t.Status == store.StatusDone {
			n++
		}
	}
	return n
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

func (m *Model) handleSentinelKey(key string) {
	switch key {
	case "up", "k":
		m.sentinelVP.LineUp(1)
	case "down", "j":
		m.sentinelVP.LineDown(1)
	case "g":
		m.sentinelVP.GotoTop()
	case "G":
		m.sentinelVP.GotoBottom()
	case "l":
		m.cycleSentinelLevelFilter()
		m.refreshSentinelVP()
	case "/":
		m.sentinelSearch = ""
		m.refreshSentinelVP()
	case "backspace":
		if len(m.sentinelSearch) > 0 {
			m.sentinelSearch = m.sentinelSearch[:len(m.sentinelSearch)-1]
			m.refreshSentinelVP()
		}
	default:
		if len(key) == 1 && key[0] >= 32 && key[0] < 127 {
			m.sentinelSearch += key
			m.refreshSentinelVP()
		}
	}
}

func (m *Model) cycleSentinelLevelFilter() {
	options := []string{"", "DEBUG", "INFO", "WARN", "ERROR"}
	m.sentinelLevelIdx = (m.sentinelLevelIdx + 1) % len(options)
	m.sentinelLevelFilter = options[m.sentinelLevelIdx]
}

func (m *Model) filteredSentinelLines() []SentinelLogRecord {
	result := make([]SentinelLogRecord, 0, len(m.sentinelLines))
	search := strings.ToLower(m.sentinelSearch)
	for _, r := range m.sentinelLines {
		if m.sentinelLevelFilter != "" && r.Level.String() != m.sentinelLevelFilter {
			continue
		}
		if search != "" {
			haystack := strings.ToLower(r.Message)
			for _, a := range r.Attrs {
				haystack += " " + strings.ToLower(a.Key) + "=" + strings.ToLower(a.Value.String())
			}
			if !strings.Contains(haystack, search) {
				continue
			}
		}
		result = append(result, r)
	}
	return result
}

func (m *Model) addSentinelLine(r SentinelLogRecord) {
	m.sentinelLines = append(m.sentinelLines, r)
	if len(m.sentinelLines) > maxSentinelLines {
		m.sentinelLines = m.sentinelLines[len(m.sentinelLines)-maxSentinelLines:]
	}
}

func (m *Model) refreshSentinelVP() {
	filtered := m.filteredSentinelLines()
	if len(filtered) == 0 {
		m.sentinelVP.SetContent(styleTimestamp.Render("No internal logs match current filters..."))
		return
	}
	var sb strings.Builder
	for _, r := range filtered {
		sb.WriteString(renderSentinelLine(r))
		sb.WriteString("\n")
	}
	m.sentinelVP.SetContent(sb.String())
	m.sentinelVP.GotoBottom()
}

func waitForSentinelLog(ctx context.Context, ch <-chan SentinelLogRecord) tea.Cmd {
	return func() tea.Msg {
		if ch == nil {
			return nil
		}
		select {
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			return sentinelLogMsg{msg}
		case <-ctx.Done():
			return nil
		}
	}
}

func waitForLog(ctx context.Context, ch <-chan poller.LogMsg) tea.Cmd {
	return func() tea.Msg {
		select {
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			return logMsg{msg}
		case <-ctx.Done():
			return nil
		}
	}
}

func waitForTicket(ctx context.Context, ch <-chan poller.TicketMsg) tea.Cmd {
	return func() tea.Msg {
		select {
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			return tickMsg{msg}
		case <-ctx.Done():
			return nil
		}
	}
}

func waitForStatus(ctx context.Context, ch <-chan poller.AppStatusMsg) tea.Cmd {
	return func() tea.Msg {
		select {
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			return statusMsg{msg}
		case <-ctx.Done():
			return nil
		}
	}
}

func waitForWorkerEvent(ctx context.Context, ch <-chan worker.Event) tea.Cmd {
	return func() tea.Msg {
		if ch == nil {
			return nil
		}
		select {
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			return workerEvMsg{msg}
		case <-ctx.Done():
			return nil
		}
	}
}

func waitForSQSStats(ctx context.Context, ch <-chan queue.QueueStats) tea.Cmd {
	return func() tea.Msg {
		if ch == nil {
			return nil
		}
		select {
		case s, ok := <-ch:
			if !ok {
				return nil
			}
			return sqsStatsMsg{s}
		case <-ctx.Done():
			return nil
		}
	}
}
