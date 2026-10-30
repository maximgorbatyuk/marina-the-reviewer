package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/usage"
)

type usageState struct {
	days     int
	loading  bool
	loadID   uint64
	err      string
	report   usage.Report
	viewport viewport.Model
}

type usageLoaded struct {
	id     uint64
	report usage.Report
	err    error
}

func (m *model) showUsage() tea.Cmd {
	m.cancelNetwork()
	m.screen = usageScreen
	m.usage.viewport.GotoTop()
	return m.loadUsage()
}

func (m *model) loadUsage() tea.Cmd {
	if m.usage.loading {
		return nil
	}
	m.usage.loading = true
	m.usage.err = ""
	m.usage.loadID++
	m.rebuildUsage()
	id, ctx := m.usage.loadID, m.ctx
	return func() tea.Msg {
		result := usageLoaded{id: id}
		if err := ctx.Err(); err != nil {
			result.err = err
			return result
		}
		store, err := usage.DefaultStore()
		if err == nil {
			result.report, err = usage.Generate(store, time.Now().UTC(), []int{7, 30, 90})
		}
		result.err = err
		return result
	}
}

func (m *model) receiveUsage(result usageLoaded) {
	if result.id != m.usage.loadID {
		return
	}
	m.usage.loading = false
	if result.err != nil {
		m.usage.err = safeText(result.err.Error())
	} else {
		m.usage.err = ""
		m.usage.report = result.report
	}
	m.rebuildUsage()
}

func (m *model) selectUsagePeriod(days int) {
	m.usage.days = days
	m.rebuildUsage()
	m.usage.viewport.GotoTop()
}

func usageTokens(n *int64) string {
	if n == nil {
		return "?"
	}
	return strconv.FormatInt(*n, 10)
}

func writeUsageTotals(b *strings.Builder, title string, t usage.Totals) {
	fmt.Fprintf(b, "%s\nInput: %s   Output: %s   Total: %s\nRequests: %d   Failed: %d\nUnknown: %d   Partial: %d   Unfinished: %d\n\n", title, usageTokens(t.InputTokens), usageTokens(t.OutputTokens), usageTokens(t.TotalTokens), t.Requests, t.FailedRequests, t.UnknownUsageRequests, t.PartialUsageRequests, t.PendingRequests)
}

func (m *model) rebuildUsage() {
	offset := m.usage.viewport.YOffset()
	var b strings.Builder
	switch {
	case m.usage.err != "":
		fmt.Fprintf(&b, "Cannot load usage.\n%s\n\nPress r to retry.\n", m.usage.err)
	case m.usage.report.GeneratedAt.IsZero():
		b.WriteString("Loading usage...\nYou can select a period or press Esc to return.\n")
	default:
		if m.usage.loading {
			b.WriteString("Refreshing usage...\n")
		}
		fmt.Fprintf(&b, "Updated: %s UTC\n", m.usage.report.GeneratedAt.Format("2006-01-02 15:04:05"))
		for _, period := range m.usage.report.Periods {
			if period.Days != m.usage.days {
				continue
			}
			fmt.Fprintf(&b, "Last %d days, from %s UTC\n\n", period.Days, period.Since.Format("2006-01-02 15:04:05"))
			r := period.Reviews
			average := "?"
			if r.AverageDurationMilliseconds != nil {
				average = fmt.Sprintf("%.0f ms", *r.AverageDurationMilliseconds)
			}
			fmt.Fprintf(&b, "Code reviews\nReviews: %d   Complete: %d   Partial: %d\nFailed: %d   Canceled: %d   Unfinished: %d\nChecked blocks: %d   Average duration: %s\nFindings — Warning: %d   Error: %d   Critical: %d\n\nToken usage by provider\n", r.Total, r.Complete, r.Partial, r.Failed, r.Canceled, r.Unfinished, r.CheckedBlocks, average, r.WarningFindings, r.ErrorFindings, r.CriticalFindings)
			if len(period.Providers) == 0 {
				b.WriteString("No API requests recorded for this period.\n\n")
			} else {
				writeUsageTotals(&b, "All providers (known tokens)", period.Total)
				for _, provider := range period.Providers {
					writeUsageTotals(&b, safeText(provider.Provider), provider.Totals)
				}
			}
			break
		}
		b.WriteString("Only reported tokens are summed; ? means unavailable.\nUnknown or partial usage makes token totals incomplete.\nUnfinished requests are included in unknown.\n")
		if m.usage.report.IncompleteTail {
			b.WriteString("\nWarning: an incomplete final journal entry was excluded during import.\n")
		}
		if m.usage.report.ReviewTrackingSince != nil {
			fmt.Fprintf(&b, "\nReview tracking since %s UTC.\n", m.usage.report.ReviewTrackingSince.Format("2006-01-02 15:04:05"))
		} else {
			b.WriteString("\nCode review tracking has not started.\nOld token history cannot supply review counts.\n")
		}
		b.WriteString("History is retained for 90 days.\nReview duration includes pauses between runs.\nUnfinished reviews have no confirmed final outcome.\n\nLocal CLI history only; earlier runs and other tools are not included.\n")
	}
	m.usage.viewport.SetContent(b.String())
	m.usage.viewport.SetYOffset(offset)
}

func (m *model) usagePeriods() string {
	items := []string{}
	for i, days := range []int{7, 30, 90} {
		label := fmt.Sprintf("%d:%dd", i+1, days)
		if days == m.usage.days {
			label = yellow.Render("[" + label + "]")
		}
		items = append(items, label)
	}
	return "Period: " + strings.Join(items, "  ")
}

func (m *model) updateUsage(message tea.Msg, key string) tea.Cmd {
	switch key {
	case "1":
		m.selectUsagePeriod(7)
	case "2":
		m.selectUsagePeriod(30)
	case "3":
		m.selectUsagePeriod(90)
	case "left", "shift+tab":
		next := map[int]int{7: 90, 30: 7, 90: 30}
		m.selectUsagePeriod(next[m.usage.days])
	case "right", "tab":
		next := map[int]int{7: 30, 30: 90, 90: 7}
		m.selectUsagePeriod(next[m.usage.days])
	case "home":
		m.usage.viewport.GotoTop()
	case "end":
		m.usage.viewport.GotoBottom()
	case "r":
		return m.loadUsage()
	case "esc":
		m.screen = homeScreen
	case "?":
		m.showHelp()
	case "q":
		return m.requestExit()
	default:
		var command tea.Cmd
		m.usage.viewport, command = m.usage.viewport.Update(message)
		return command
	}
	return nil
}
