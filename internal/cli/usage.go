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
		fmt.Fprintln(out, "usage: marinadoreview usage [--period 7d|30d|90d] [--json]\n\nShows locally recorded API usage. Without --period, shows all three windows.\nMARINADOREVIEW_USAGE_FILE selects another private usage journal.")
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
			fmt.Fprintln(errOut, "usage period must be 7d, 30d or 90d")
			return 2
		}
		days = []int{value}
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(errOut, "usage accepts only --period and --json")
		return 2
	}
	path, err := usage.DefaultPath()
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 5
	}
	report, err := usage.Generate(usage.Store{Path: path}, time.Now().UTC(), days)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 5
	}
	if *jsonOutput {
		err = json.NewEncoder(out).Encode(report)
	} else {
		err = printUsage(out, report)
	}
	if err != nil {
		fmt.Fprintln(errOut, "cannot write usage report")
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
		if len(period.Providers) == 0 {
			if _, err := fmt.Fprint(out, "No API requests recorded.\n\n"); err != nil {
				return err
			}
			continue
		}
		table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(table, "PROVIDER\tREQUESTS\tINPUT\tOUTPUT\tTOTAL\tUNKNOWN\tPARTIAL\tUNFINISHED")
		for _, p := range period.Providers {
			fmt.Fprintf(table, "%s\t%d\t%s\t%s\t%s\t%d\t%d\t%d\n", p.Provider, p.Requests, usageNumber(p.InputTokens), usageNumber(p.OutputTokens), usageNumber(p.TotalTokens), p.UnknownUsageRequests, p.PartialUsageRequests, p.PendingRequests)
		}
		t := period.Total
		fmt.Fprintf(table, "TOTAL\t%d\t%s\t%s\t%s\t%d\t%d\t%d\n", t.Requests, usageNumber(t.InputTokens), usageNumber(t.OutputTokens), usageNumber(t.TotalTokens), t.UnknownUsageRequests, t.PartialUsageRequests, t.PendingRequests)
		if err := table.Flush(); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
	}
	if report.IncompleteTail {
		if _, err := fmt.Fprintln(out, "Warning: an incomplete final journal entry was excluded."); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(out, "Only API-reported tokens are summed; ? means unavailable. UNKNOWN and PARTIAL requests make totals incomplete.\nUNFINISHED requests are included in UNKNOWN. Reviews, retries, fallback and TUI tests are tracked on this machine.\nEarlier runs and calls from other tools are not included.")
	return err
}
