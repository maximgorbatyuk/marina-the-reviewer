package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/usage"
)

func usageCommand(args []string, out, errOut io.Writer) int {
	if slices.Contains(args, "--help") || slices.Contains(args, "-h") {
		_, _ = fmt.Fprintln(out, "usage: marinadoreview usage [--period 7d|30d|90d] [--json]\n\nShows local token and code review statistics, retained for 90 days. Without --period, shows all three windows.\nMARINADOREVIEW_DB_FILE selects another private SQLite database.")
		return 0
	}
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(errOut)
	period := fs.String("period", "", "rolling window: 7d, 30d or 90d")
	jsonOutput := fs.Bool("json", false, "write a JSON report")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	days := []int{7, 30, 90}
	if *period != "" {
		value, ok := map[string]int{"7d": 7, "30d": 30, "90d": 90}[*period]
		if !ok {
			_, _ = fmt.Fprintln(errOut, "usage period must be 7d, 30d or 90d")
			return 2
		}
		days = []int{value}
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintln(errOut, "usage accepts only --period and --json")
		return 2
	}
	store, err := usage.DefaultStore()
	if err != nil {
		_, _ = fmt.Fprintln(errOut, err)
		return 5
	}
	report, err := usage.Generate(store, time.Now().UTC(), days)
	if err != nil {
		_, _ = fmt.Fprintln(errOut, err)
		return 5
	}
	if *jsonOutput {
		err = json.NewEncoder(out).Encode(report)
	} else {
		err = printUsage(out, report)
	}
	if err != nil {
		_, _ = fmt.Fprintln(errOut, "cannot write usage report")
		return 5
	}
	return 0
}

func usageNumber(n *int64) string {
	if n == nil {
		return "?"
	}
	return strconv.FormatInt(*n, 10)
}

func printUsage(out io.Writer, report usage.Report) error {
	for _, period := range report.Periods {
		if _, err := fmt.Fprintf(out, "Last %d days (%s to %s, UTC)\n", period.Days, period.Since.Format(time.RFC3339), report.GeneratedAt.Format(time.RFC3339)); err != nil {
			return err
		}
		r := period.Reviews
		average := "?"
		if r.AverageDurationMilliseconds != nil {
			average = fmt.Sprintf("%.0f ms", *r.AverageDurationMilliseconds)
		}
		if _, err := fmt.Fprintf(out, "Code reviews\nReviews: %d   Complete: %d   Partial: %d\nFailed: %d   Canceled: %d   Unfinished: %d\nChecked blocks: %d   Average duration: %s\nFindings — Warning: %d   Error: %d   Critical: %d\n\nToken usage by provider\n", r.Total, r.Complete, r.Partial, r.Failed, r.Canceled, r.Unfinished, r.CheckedBlocks, average, r.WarningFindings, r.ErrorFindings, r.CriticalFindings); err != nil {
			return err
		}
		if len(period.Providers) == 0 {
			if _, err := fmt.Fprint(out, "No API requests recorded.\n\n"); err != nil {
				return err
			}
			continue
		}
		table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(table, "PROVIDER\tREQUESTS\tINPUT\tOUTPUT\tTOTAL\tUNKNOWN\tPARTIAL\tUNFINISHED")
		for _, p := range period.Providers {
			_, _ = fmt.Fprintf(table, "%s\t%d\t%s\t%s\t%s\t%d\t%d\t%d\n", p.Provider, p.Requests, usageNumber(p.InputTokens), usageNumber(p.OutputTokens), usageNumber(p.TotalTokens), p.UnknownUsageRequests, p.PartialUsageRequests, p.PendingRequests)
		}
		t := period.Total
		_, _ = fmt.Fprintf(table, "TOTAL\t%d\t%s\t%s\t%s\t%d\t%d\t%d\n", t.Requests, usageNumber(t.InputTokens), usageNumber(t.OutputTokens), usageNumber(t.TotalTokens), t.UnknownUsageRequests, t.PartialUsageRequests, t.PendingRequests)
		if err := table.Flush(); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
	}
	if report.IncompleteTail {
		if _, err := fmt.Fprintln(out, "Warning: an incomplete final journal entry was excluded during import."); err != nil {
			return err
		}
	}
	if report.ReviewTrackingSince != nil {
		if _, err := fmt.Fprintf(out, "Review tracking since %s (UTC).\n", report.ReviewTrackingSince.Format(time.RFC3339)); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(out, "Code review tracking has not started; old token history cannot supply review counts."); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(out, "History is retained for 90 days; expired records are removed when storage is used.\nOnly API-reported tokens are summed; ? means unavailable. UNKNOWN and PARTIAL requests make totals incomplete.\nUNFINISHED requests are included in UNKNOWN. Retries, fallback and TUI tests are API attempts; resume does not add a review.\nReview duration includes pauses between runs. Unfinished reviews have no confirmed final outcome.\nEarlier runs and calls from other tools are not included.")
	return err
}
