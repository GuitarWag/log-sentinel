package tui

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/log-sentinel/sentinel/internal/sources"
	"github.com/log-sentinel/sentinel/internal/store"
	"github.com/log-sentinel/sentinel/internal/worker"
)

var (
	colorRed    = lipgloss.Color("196")
	colorOrange = lipgloss.Color("208")
	colorYellow = lipgloss.Color("220")
	colorBlue   = lipgloss.Color("39")
	colorGreen  = lipgloss.Color("82")
	colorGray   = lipgloss.Color("240")
	colorWhite  = lipgloss.Color("255")
	colorCyan   = lipgloss.Color("51")
	colorDark   = lipgloss.Color("235")
	colorDarker = lipgloss.Color("233")
	colorBorder = lipgloss.Color("238")
)

var (
	stylePanelBorder = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorBorder)

	stylePanelBorderFocused = lipgloss.NewStyle().
					Border(lipgloss.RoundedBorder()).
					BorderForeground(colorCyan)

	stylePanelTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorWhite).
			Background(colorDark).
			Padding(0, 1)

	styleHeaderBar = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorWhite).
			Background(colorDark).
			Padding(0, 1)

	styleFooter = lipgloss.NewStyle().
			Foreground(colorGray).
			Background(colorDarker).
			Padding(0, 1)

	styleTabActive = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("235")).
			Background(colorCyan).
			Padding(0, 2)

	styleTabInactive = lipgloss.NewStyle().
				Foreground(colorGray).
				Background(colorDark).
				Padding(0, 2)

	styleFilterOn  = lipgloss.NewStyle().Foreground(colorCyan).Bold(true)
	styleFilterOff = lipgloss.NewStyle().Foreground(colorGray)

	styleStatusPolling    = lipgloss.NewStyle().Foreground(colorYellow)
	styleStatusIdle       = lipgloss.NewStyle().Foreground(colorGreen)
	styleStatusError      = lipgloss.NewStyle().Foreground(colorRed)
	styleWorkerProcessing = lipgloss.NewStyle().Foreground(colorYellow)
	styleWorkerDone       = lipgloss.NewStyle().Foreground(colorGreen)
	styleWorkerFailed     = lipgloss.NewStyle().Foreground(colorRed)

	styleTicketOpen       = lipgloss.NewStyle().Foreground(colorOrange)
	styleTicketInProgress = lipgloss.NewStyle().Foreground(colorYellow)
	styleTicketDone       = lipgloss.NewStyle().Foreground(colorGreen)
	styleTicketFailed     = lipgloss.NewStyle().Foreground(colorRed)
	styleTicketPending    = lipgloss.NewStyle().Foreground(colorGray)

	styleSeverityCritical = lipgloss.NewStyle().Foreground(colorRed).Bold(true)
	styleSeverityHigh     = lipgloss.NewStyle().Foreground(colorOrange).Bold(true)
	styleSeverityMedium   = lipgloss.NewStyle().Foreground(colorYellow)
	styleSeverityLow      = lipgloss.NewStyle().Foreground(colorBlue)

	styleAppName   = lipgloss.NewStyle().Foreground(colorCyan).Bold(true)
	styleTimestamp = lipgloss.NewStyle().Foreground(colorGray)
	styleCount     = lipgloss.NewStyle().Foreground(colorOrange).Bold(true)
	styleCursor    = lipgloss.NewStyle().Foreground(colorCyan).Bold(true)
)

func RenderLayout(m Model) string {
	if m.width == 0 {
		return "Initializing..."
	}

	header := renderHeaderBar(m)
	tabBar := renderTabBar(m)
	footer := renderFooterBar(m)

	contentH := m.height - 4
	if contentH < 1 {
		contentH = 1
	}
	contentW := m.width

	var content string
	switch m.activeTab {
	case 0:
		content = renderOverviewTab(m, contentW, contentH)
	case 1:
		content = renderLogsTab(m, contentW, contentH)
	case 2:
		content = renderTicketsTab(m, contentW, contentH)
	case 3:
		content = renderWorkersTab(m, contentW, contentH)
	case 4:
		content = renderSentinelTab(m, contentW, contentH)
	}

	return lipgloss.JoinVertical(lipgloss.Left, header, tabBar, content, footer)
}

func renderHeaderBar(m Model) string {
	title := "Log Sentinel"
	open := m.statsOpen()
	active := m.statsActive()
	done := m.statsDone()
	stats := fmt.Sprintf("Open:%d  Active:%d  Done:%d", open, active, done)
	if m.pollersPaused != nil && m.pollersPaused.Load() {
		stats += "  " + styleSeverityMedium.Render("[POLLERS PAUSED]")
	}
	if m.workersPaused != nil && m.workersPaused.Load() {
		stats += "  " + styleSeverityMedium.Render("[WORKERS PAUSED]")
	}

	titlePart := title
	gap := m.width - lipgloss.Width(titlePart) - lipgloss.Width(stats) - 2
	if gap < 1 {
		gap = 1
	}
	line := titlePart + strings.Repeat(" ", gap) + stats
	return styleHeaderBar.Width(m.width).Render(line)
}

func renderTabBar(m Model) string {
	ticketCount := len(m.tickets)
	labels := []string{
		"[1] Overview",
		"[2] Logs",
		fmt.Sprintf("[3] Tickets (%d)", ticketCount),
		"[4] Workers",
		"[5] Sentinel",
	}

	tabs := make([]string, len(labels))
	for i, label := range labels {
		if i == m.activeTab {
			tabs[i] = styleTabActive.Render(label)
		} else {
			tabs[i] = styleTabInactive.Render(label)
		}
	}
	bar := lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
	barW := lipgloss.Width(bar)
	if barW < m.width {
		bar += lipgloss.NewStyle().Background(colorDark).Render(strings.Repeat(" ", m.width-barW))
	}
	return bar
}

func renderFooterBar(m Model) string {
	var help string
	switch m.activeTab {
	case 0:
		help = "Tab/1-5: switch  p: pause pollers  w: pause workers  q: quit"
	case 1:
		help = "Tab/1-5: switch  ↑↓/jk: scroll  g/G: top/bottom  a: filter app  q: quit"
	case 2:
		if m.showDetail {
			help = "↑↓/jk: scroll  Esc: back  q: quit"
		} else {
			help = "Tab/1-5: switch  ↑↓/jk: select  Enter: detail  f: status  a: app  s: sev  q: quit"
		}
	case 3:
		help = "Tab/1-5: switch  ↑↓/jk: scroll  g/G: top/bottom  a: filter app  p: pause pollers  w: pause workers  q: quit"
	case 4:
		help = "Tab/1-5: switch  ↑↓/jk: scroll  g/G: top/bottom  l: level  type to search  q: quit"
	}
	return styleFooter.Width(m.width).Render(help)
}

func renderOverviewTab(m Model, w, h int) string {
	halfW := w / 2
	if halfW < 10 {
		halfW = 10
	}

	var leftSB strings.Builder
	leftSB.WriteString(stylePanelTitle.Render("Applications") + "\n\n")
	if len(m.appOrder) == 0 {
		leftSB.WriteString(styleTimestamp.Render("No applications configured") + "\n")
	}
	for _, name := range m.appOrder {
		s := m.appStatuses[name]
		if s == nil {
			continue
		}
		statusStr := renderAppStatus(s)
		leftSB.WriteString(styleAppName.Render(truncate(name, halfW-4)) + "\n")
		leftSB.WriteString("  " + statusStr + "\n")
		if s.ErrorMsg != "" {
			leftSB.WriteString(styleStatusError.Render("  "+truncate(s.ErrorMsg, halfW-6)) + "\n")
		}
		leftSB.WriteString("\n")
	}
	leftContent := padToHeight(leftSB.String(), h-2)
	leftPanel := stylePanelBorder.Width(halfW - 4).Height(h - 2).Render(leftContent)

	open := m.statsOpen()
	active := m.statsActive()
	done := m.statsDone()
	failed := 0
	for _, t := range m.tickets {
		if t.Status == store.StatusFailed {
			failed++
		}
	}
	pending := 0
	for _, t := range m.tickets {
		if t.Status == store.StatusPending {
			pending++
		}
	}

	var rightSB strings.Builder
	rightSB.WriteString(stylePanelTitle.Render("Ticket Statistics") + "\n\n")
	rightSB.WriteString(styleTicketOpen.Render("■ ") + styleTicketOpen.Render(fmt.Sprintf("Open        %d", open)) + "\n\n")
	rightSB.WriteString(styleTicketInProgress.Render("■ ") + styleTicketInProgress.Render(fmt.Sprintf("In Progress %d", active)) + "\n\n")
	rightSB.WriteString(styleTicketDone.Render("■ ") + styleTicketDone.Render(fmt.Sprintf("Done        %d", done)) + "\n\n")
	rightSB.WriteString(styleTicketFailed.Render("■ ") + styleTicketFailed.Render(fmt.Sprintf("Failed      %d", failed)) + "\n\n")
	rightSB.WriteString(styleTicketPending.Render("■ ") + styleTicketPending.Render(fmt.Sprintf("Pending     %d", pending)) + "\n\n")
	rightSB.WriteString(styleTimestamp.Render(fmt.Sprintf("Total       %d", len(m.tickets))) + "\n")

	rightContent := padToHeight(rightSB.String(), h-2)
	rightW := w - halfW - 2
	if rightW < 10 {
		rightW = 10
	}
	rightPanel := stylePanelBorder.Width(rightW - 4).Height(h - 2).Render(rightContent)

	return lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, rightPanel)
}

func renderLogsTab(m Model, w, h int) string {
	innerW := w - 4
	if innerW < 1 {
		innerW = 1
	}

	filterBar := renderLogFilterBar(m, innerW)

	vp := m.logVP
	vp.Width = innerW
	vp.Height = h - 6
	if vp.Height < 1 {
		vp.Height = 1
	}

	content := filterBar + "\n" + vp.View()
	return stylePanelBorder.Width(innerW).Height(h - 2).Render(content)
}

func renderLogFilterBar(m Model, width int) string {
	appLabel := "all"
	if m.logAppFilter != "" {
		appLabel = m.logAppFilter
	}

	appPart := styleFilterOff.Render("[a] App: ") + styleFilterOn.Render(appLabel)
	return appPart
}

func renderTicketsTab(m Model, w, h int) string {
	if m.showDetail {
		return renderDetailView(m, w, h)
	}
	return renderTicketList(m, w, h)
}

func renderTicketList(m Model, w, h int) string {
	innerW := w - 4
	if innerW < 1 {
		innerW = 1
	}
	innerH := h - 2

	filterBar := renderTicketFilterBar(m, innerW)

	filtered := m.filteredTickets()
	visible := m.ticketListVisibleItems()

	end := m.ticketOffset + visible
	if end > len(filtered) {
		end = len(filtered)
	}
	page := filtered
	if m.ticketOffset < len(filtered) {
		page = filtered[m.ticketOffset:end]
	} else {
		page = nil
	}

	var sb strings.Builder
	sb.WriteString(filterBar + "\n")

	if len(filtered) == 0 {
		sb.WriteString("\n" + styleTimestamp.Render("  No tickets match current filters.") + "\n")
	} else {
		for i, t := range page {
			absIdx := m.ticketOffset + i
			cursor := "  "
			if absIdx == m.ticketCursor {
				cursor = styleCursor.Render("▶ ")
			}

			sev := renderSeverityBadge(t.Severity)
			status := renderTicketStatus(t.Status)
			app := styleAppName.Render(truncate(t.App, 16))
			count := styleCount.Render(fmt.Sprintf("×%d", t.OccurrenceCount))
			ticketID := styleTimestamp.Render(fmt.Sprintf("#%s", itoa(t.ID)))

			line1 := cursor + sev + " " + status + " " + app + "  " + count + "  " + ticketID
			cls := truncate(t.Classification, 30)
			comp := styleTimestamp.Render(truncate(t.Component, 14))
			lastSeen := styleTimestamp.Render(formatRelativeTime(t.LastSeen))
			line2 := "    " + cls + "  " + comp + "  " + lastSeen
			divider := styleTimestamp.Render(strings.Repeat("·", innerW-2))

			sb.WriteString(line1 + "\n")
			sb.WriteString(line2 + "\n")
			sb.WriteString(divider + "\n")
		}

		total := len(filtered)
		if total > visible {
			shown := end
			indicator := styleTimestamp.Render(fmt.Sprintf("  %d-%d of %d  (↑↓ to scroll)", m.ticketOffset+1, shown, total))
			sb.WriteString(indicator + "\n")
		}
	}

	content := padToHeight(sb.String(), innerH)
	return stylePanelBorderFocused.Width(innerW).Height(innerH).Render(content)
}

func renderTicketFilterBar(m Model, width int) string {
	statusLabel := "All"
	if m.ticketStatusFilter != "" {
		statusLabel = m.ticketStatusFilter
	}
	appLabel := "all"
	if m.ticketAppFilter != "" {
		appLabel = m.ticketAppFilter
	}
	sevLabel := "all"
	if m.ticketSevFilter != "" {
		sevLabel = m.ticketSevFilter
	}

	parts := []string{
		styleFilterOff.Render("[f] Status: ") + styleFilterOn.Render(statusLabel),
		styleFilterOff.Render("  [a] App: ") + styleFilterOn.Render(appLabel),
		styleFilterOff.Render("  [s] Sev: ") + styleFilterOn.Render(sevLabel),
	}
	return strings.Join(parts, "")
}

func renderDetailView(m Model, w, h int) string {
	innerW := w - 4
	if innerW < 1 {
		innerW = 1
	}
	innerH := h - 2

	backHdr := styleTimestamp.Render("← Back") + "  " + styleFilterOff.Render("[Esc]")

	vp := m.detailVP
	vp.Width = innerW - 2
	vp.Height = innerH - 3
	if vp.Height < 1 {
		vp.Height = 1
	}

	content := backHdr + "\n" + strings.Repeat("─", innerW-2) + "\n" + vp.View()
	return stylePanelBorderFocused.Width(innerW).Height(innerH).Render(content)
}

func renderWorkersTab(m Model, w, h int) string {
	innerW := w - 4
	if innerW < 1 {
		innerW = 1
	}

	sqsBar := renderSQSStats(m, innerW)
	filterBar := renderWorkerFilterBar(m, innerW)

	vp := m.workerVP
	vp.Width = innerW
	vp.Height = h - 7
	if vp.Height < 1 {
		vp.Height = 1
	}

	content := sqsBar + "\n" + filterBar + "\n" + vp.View()
	return stylePanelBorder.Width(innerW).Height(h - 2).Render(content)
}

func renderSQSStats(m Model, width int) string {
	stats := m.sqsStats
	queued := styleCount.Render(fmt.Sprintf("%d", stats.ApproxMessages))
	inflight := styleTimestamp.Render(fmt.Sprintf("%d in-flight", stats.InFlight))
	return styleFilterOff.Render("SQS: ") + queued + styleFilterOff.Render(" queued  ") + inflight
}

func renderWorkerFilterBar(m Model, width int) string {
	appLabel := "all"
	if m.workerAppFilter != "" {
		appLabel = m.workerAppFilter
	}
	return styleFilterOff.Render("[a] App: ") + styleFilterOn.Render(appLabel)
}

func renderLogLine(entry sources.LogEntry) string {
	ts := styleTimestamp.Render(entry.Timestamp.Format("15:04:05"))
	app := styleAppName.Render("[" + entry.AppName + "]")
	sev := renderSeverityBadge(entry.Severity)
	msg := entry.Message
	if msg == "" {
		msg = entry.RawLine
	}
	msg = truncate(msg, 120)
	return fmt.Sprintf("%s %s %s %s", ts, app, sev, msg)
}

func renderWorkerEventLine(e worker.Event) string {
	ts := styleTimestamp.Render(e.Timestamp.Format("15:04:05"))
	app := styleAppName.Render("[" + truncate(e.App, 14) + "]")
	action := styleTimestamp.Render(truncate(e.ActionName, 20))
	cls := truncate(e.Classification, 28)

	var statusStr string
	switch e.Status {
	case "processing":
		statusStr = styleWorkerProcessing.Render("⟳ running")
	case "done":
		statusStr = styleWorkerDone.Render("✓ done   ")
	case "failed":
		errPart := ""
		if e.ErrMsg != "" {
			errPart = "  " + styleStatusError.Render(truncate(e.ErrMsg, 30))
		}
		statusStr = styleWorkerFailed.Render("✗ failed ") + errPart
	}

	ticketPart := ""
	if e.TicketID > 0 {
		ticketPart = "  " + styleCount.Render(fmt.Sprintf("#%s", itoa(e.TicketID)))
	}

	return fmt.Sprintf("%s %s %s  %s%s  %s", ts, app, statusStr, action, ticketPart, cls)
}

func renderTicketStatus(status string) string {
	switch status {
	case "open":
		return styleTicketOpen.Render("[TODO]")
	case "in_progress":
		return styleTicketInProgress.Render("[WORK]")
	case "done":
		return styleTicketDone.Render("[DONE]")
	case "failed":
		return styleTicketFailed.Render("[FAIL]")
	case "pending":
		return styleTicketPending.Render("[PEND]")
	default:
		return styleTimestamp.Render("[    ]")
	}
}

func renderAppStatus(s *appStatus) string {
	switch s.Status {
	case "polling":
		return styleStatusPolling.Render("⟳ polling")
	case "error":
		return styleStatusError.Render("✗ error")
	default:
		return styleStatusIdle.Render("✓ idle")
	}
}

func renderSeverityBadge(severity string) string {
	upper := strings.ToUpper(severity)
	switch upper {
	case "CRITICAL", "FATAL":
		return styleSeverityCritical.Render("[CRIT]")
	case "HIGH", "ERROR":
		return styleSeverityHigh.Render("[HIGH]")
	case "MEDIUM", "WARNING", "WARN":
		return styleSeverityMedium.Render("[MED] ")
	default:
		return styleSeverityLow.Render("[LOW] ")
	}
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 1 {
		return string(runes[:n])
	}
	return string(runes[:n-1]) + "…"
}

func padToHeight(content string, height int) string {
	lines := strings.Split(content, "\n")
	for len(lines) < height {
		lines = append(lines, "")
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

func formatRelativeTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("Jan 2")
	}
}

func renderSentinelTab(m Model, w, h int) string {
	innerW := w - 4
	if innerW < 1 {
		innerW = 1
	}
	vp := m.sentinelVP
	vp.Width = innerW
	vp.Height = h - 6
	if vp.Height < 1 {
		vp.Height = 1
	}
	filterBar := renderSentinelFilterBar(m, innerW)
	content := filterBar + "\n" + vp.View()
	return stylePanelBorder.Width(innerW).Height(h - 2).Render(content)
}

func renderSentinelFilterBar(m Model, width int) string {
	levelLabel := "all"
	if m.sentinelLevelFilter != "" {
		levelLabel = m.sentinelLevelFilter
	}
	filtered := m.filteredSentinelLines()
	total := len(m.sentinelLines)
	counts := styleTimestamp.Render(fmt.Sprintf("%d/%d", len(filtered), total))
	searchPart := styleFilterOff.Render("  /search: ")
	if m.sentinelSearch != "" {
		searchPart += styleFilterOn.Render(m.sentinelSearch)
	} else {
		searchPart += styleTimestamp.Render("─")
	}
	return styleFilterOff.Render("[l] Level: ") + styleFilterOn.Render(levelLabel) + searchPart + "  " + counts
}

func renderSentinelLine(r SentinelLogRecord) string {
	ts := styleTimestamp.Render(r.Time.Format("15:04:05"))
	var levelStyle lipgloss.Style
	switch r.Level {
	case slog.LevelError:
		levelStyle = styleSeverityHigh
	case slog.LevelWarn:
		levelStyle = styleSeverityMedium
	case slog.LevelDebug:
		levelStyle = styleTimestamp
	default:
		levelStyle = styleStatusIdle
	}
	level := levelStyle.Render(fmt.Sprintf("%-5s", r.Level.String()))
	component := sentinelComponent(r.Attrs)
	compBadge := ""
	if component != "" {
		compBadge = " " + styleAppName.Render("["+truncate(component, 12)+"]")
	}
	var attrs strings.Builder
	for _, a := range r.Attrs {
		if a.Key == "worker" || a.Key == "app" || a.Key == "source" || a.Key == "goroutine" {
			continue
		}
		attrs.WriteString("  ")
		attrs.WriteString(styleTimestamp.Render(a.Key + "="))
		attrs.WriteString(truncate(a.Value.String(), 50))
	}
	return fmt.Sprintf("%s %s%s %s%s", ts, level, compBadge, r.Message, attrs.String())
}

func sentinelComponent(attrs []slog.Attr) string {
	for _, a := range attrs {
		switch a.Key {
		case "worker":
			return "worker:" + a.Value.String()
		case "app":
			return a.Value.String()
		case "source":
			return "src:" + a.Value.String()
		}
	}
	return ""
}
