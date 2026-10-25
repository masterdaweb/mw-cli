package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// ── Metrics ───────────────────────────────────────────────────────────────

func metricsCmd(a *App) *cobra.Command {
	var service, project, window string
	cmd := &cobra.Command{
		Use:   "metrics",
		Short: "A service's requests, response times, instances, CPU and memory (and the size it should have)",
		Long: "What a service did over the last hour (or --range 6h, 24h, 7d): requests and errors,\n" +
			"response times, instances, CPU and memory against its size, restarts and cold starts —\n" +
			"and, when the past week says so, the size it should have instead.",
		Example: "  mw metrics\n  mw metrics --service api --range 24h",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch window {
			case "1h", "6h", "24h", "7d":
			default:
				return errors.New("--range is 1h, 6h, 24h or 7d")
			}
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var m api.ServiceMetrics
			if err := c.Do(cmd.Context(), "GET", "/serverless/services/"+s.ID+"/metrics", url.Values{"range": {window}}, nil, &m); err != nil {
				return err
			}
			// The recommendation is a bonus: its absence never fails the command.
			var rec api.ServiceRecommendation
			if err := c.Do(cmd.Context(), "GET", "/serverless/services/"+s.ID+"/recommendation", nil, nil, &rec); err != nil {
				rec.Recommendation = nil
			}
			out := struct {
				Metrics        api.ServiceMetrics      `json:"metrics"`
				Recommendation *api.SizeRecommendation `json:"recommendation"`
			}{m, rec.Recommendation}
			return a.print(out, func(w *tabwriter.Writer) {
				if !m.Available {
					fmt.Fprintf(w, "The metrics of %s could not be read just now: try again in a minute.\n", s.Name)
					return
				}
				fmt.Fprintf(w, "%s, the last %s (a point every %s)\n\n", s.Name, rangeText(m.Range), stepText(m.StepSeconds))
				if m.Requests != nil {
					all := sum3(m.Requests.OK, m.Requests.ClientErrors, m.Requests.ServerErrors)
					fmt.Fprintf(w, "Requests\t%s (%s a second on average, %s at the busiest)\n", count(m.Totals.Requests), rate(avg(all)), rate(peak(all)))
					errs := fmt.Sprintf("%s server errors (5xx)", count(m.Totals.ServerErrors))
					if m.Totals.ErrorRate != nil {
						errs += fmt.Sprintf(", %s of the requests", percent(*m.Totals.ErrorRate))
					}
					clientErrors := float64(m.StepSeconds) * total(m.Requests.ClientErrors)
					fmt.Fprintf(w, "Errors\t%s; %s client errors (4xx)\n", errs, count(&clientErrors))
				}
				if m.LatencyMS != nil {
					fmt.Fprintf(w, "Response time\tmedian %s, 95%% under %s (slowest point %s)\n",
						msText(last(m.LatencyMS.P50)), msText(last(m.LatencyMS.P95)), msText(peak(m.LatencyMS.P95)))
				}
				fmt.Fprintf(w, "Instances\t%s now, %s at most\n", count(last(m.Instances)), count(peak(m.Instances)))
				fmt.Fprintf(w, "CPU\t%s vCPU on average, %s at the busiest, of %s vCPU\n", vcpuText(avg(m.CPU.Avg)), vcpuText(peak(m.CPU.Max)), vcpuText(&m.Size.VCPU))
				fmt.Fprintf(w, "Memory\t%s on average, %s at the busiest, of %s\n", memBytesText(avg(m.MemoryBytes.Avg)), memBytesText(peak(m.MemoryBytes.Max)), memBytesText(&m.Size.MemoryBytes))
				restarts := fmt.Sprintf("%d", m.Totals.Restarts)
				if m.Totals.OutOfMemory > 0 {
					restarts += fmt.Sprintf(" (%d out of memory)", m.Totals.OutOfMemory)
				}
				fmt.Fprintf(w, "Restarts\t%s\n", restarts)
				if m.Kind == "WEB" || m.Kind == "PRIVATE" {
					fmt.Fprintf(w, "Cold starts\t%d (from no instance to one)\n", m.Totals.ColdStarts)
				}
				if m.Totals.EgressBytes != nil {
					fmt.Fprintf(w, "Sent to visitors\t%s\n", bytesText(strconv.FormatFloat(*m.Totals.EgressBytes, 'f', 0, 64)))
				}
				if r := rec.Recommendation; r != nil {
					fmt.Fprintf(w, "\n%s\n", recommendationText(r))
					fmt.Fprintf(w, "  `mw scale --size %s` changes it, with a new revision and no downtime.\n", r.To)
				}
			})
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().StringVar(&window, "range", "1h", "how far back: 1h, 6h, 24h or 7d")
	return cmd
}

// recommendationText says what size the past week asks for, and why.
func recommendationText(r *api.SizeRecommendation) string {
	why := ""
	switch r.Reason {
	case "out_of_memory":
		why = fmt.Sprintf("instances ran out of memory %d times", int(r.Measure))
	case "memory_high":
		why = fmt.Sprintf("memory reached %s of what it has", percent(r.Measure))
	case "cpu_high":
		why = fmt.Sprintf("CPU reached %s of what it has", percent(r.Measure))
	case "underused":
		why = fmt.Sprintf("it used at most %s of what it has", percent(r.Measure))
	default:
		why = r.Reason
	}
	verb := "go up"
	if r.Direction == "down" {
		verb = "go down"
	}
	cost := ""
	if r.MonthlyDeltaMicroCents != nil {
		if strings.HasPrefix(*r.MonthlyDeltaMicroCents, "-") {
			cost = fmt.Sprintf("; it saves about %s a month", money(strings.TrimPrefix(*r.MonthlyDeltaMicroCents, "-"), 2))
		} else if *r.MonthlyDeltaMicroCents != "0" {
			cost = fmt.Sprintf("; about %s more a month", money(*r.MonthlyDeltaMicroCents, 2))
		}
	}
	return fmt.Sprintf("Size: %s from %s to %s — %s (the last %d days)%s.", verb, r.From, r.To, why, r.Days, cost)
}

func rangeText(r string) string {
	switch r {
	case "1h":
		return "hour"
	case "6h":
		return "6 hours"
	case "24h":
		return "24 hours"
	case "7d":
		return "7 days"
	}
	return r
}

func stepText(seconds int) string {
	d := time.Duration(seconds) * time.Second
	if d >= time.Hour && d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", seconds)
}

// The series have gaps (null) where nothing was measured.
func values(series []*float64) []float64 {
	var out []float64
	for _, v := range series {
		if v != nil && !math.IsNaN(*v) {
			out = append(out, *v)
		}
	}
	return out
}

func avg(series []*float64) *float64 {
	vs := values(series)
	if len(vs) == 0 {
		return nil
	}
	t := 0.0
	for _, v := range vs {
		t += v
	}
	r := t / float64(len(vs))
	return &r
}

func peak(series []*float64) *float64 {
	vs := values(series)
	if len(vs) == 0 {
		return nil
	}
	m := vs[0]
	for _, v := range vs[1:] {
		m = math.Max(m, v)
	}
	return &m
}

func last(series []*float64) *float64 {
	for i := len(series) - 1; i >= 0; i-- {
		if series[i] != nil {
			return series[i]
		}
	}
	return nil
}

// sum3 adds three series point by point (a point missing in all is missing).
func sum3(a, b, c []*float64) []*float64 {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	if len(c) > n {
		n = len(c)
	}
	out := make([]*float64, n)
	for i := 0; i < n; i++ {
		var t float64
		seen := false
		for _, s := range [][]*float64{a, b, c} {
			if i < len(s) && s[i] != nil {
				t += *s[i]
				seen = true
			}
		}
		if seen {
			v := t
			out[i] = &v
		}
	}
	return out
}

// total adds a series' points (rates a second, times the step: a count).
func total(series []*float64) float64 {
	t := 0.0
	for _, v := range values(series) {
		t += v
	}
	return t
}

func count(p *float64) string {
	if p == nil {
		return "—"
	}
	return strconv.FormatFloat(math.Round(*p), 'f', 0, 64)
}

func rate(p *float64) string {
	if p == nil {
		return "—"
	}
	if *p >= 10 {
		return strconv.FormatFloat(*p, 'f', 0, 64)
	}
	return strconv.FormatFloat(*p, 'f', 2, 64)
}

// percent prints a share (0.05) as a percentage (5%), to two decimals at most.
func percent(share float64) string {
	return strconv.FormatFloat(math.Round(share*10000)/100, 'f', -1, 64) + "%"
}

func msText(p *float64) string {
	if p == nil {
		return "—"
	}
	if *p >= 1000 {
		return strconv.FormatFloat(*p/1000, 'f', 1, 64) + " s"
	}
	return strconv.FormatFloat(math.Round(*p), 'f', 0, 64) + " ms"
}

func vcpuText(p *float64) string {
	if p == nil {
		return "—"
	}
	return strconv.FormatFloat(math.Round(*p*100)/100, 'f', -1, 64)
}

// memBytesText prints memory as instances have it: binary units (MiB, GiB).
func memBytesText(p *float64) string {
	if p == nil {
		return "—"
	}
	if *p >= 1<<30 {
		return strconv.FormatFloat(*p/(1<<30), 'f', 1, 64) + " GiB"
	}
	return strconv.FormatFloat(*p/(1<<20), 'f', 0, 64) + " MiB"
}

// ── Access logs (`mw logs --access`) ───────────────────────────────────────

// accessLogs prints the requests a web or private service answered, and with
// follow keeps printing new ones (the API is read again every two seconds).
func (a *App) accessLogs(cmd *cobra.Command, s *api.Service, from, search, status string, limit int, follow bool) error {
	if s.Kind != "WEB" && s.Kind != "PRIVATE" {
		return fmt.Errorf("%s is a %s: it answers no requests, so it has no access logs (its own logs: `mw logs`)", s.Name, strings.ToLower(s.Kind))
	}
	c, _ := a.Client()
	last := ""
	// Requests at the very instant the next read starts from come back again.
	printed := map[string]bool{}
	for {
		q := url.Values{"limit": {strconv.Itoa(limit)}}
		if from != "" {
			q.Set("since", from)
		}
		if search != "" {
			q.Set("search", search)
		}
		if status != "" {
			q.Set("status", status)
		}
		var logs api.AccessLogs
		if err := c.Do(cmd.Context(), "GET", "/serverless/services/"+s.ID+"/access-logs", q, nil, &logs); err != nil {
			return err
		}
		for _, l := range logs.Lines {
			key := fmt.Sprintf("%s|%s|%s|%s|%d|%d", l.TS, l.Instance, l.Method, l.Path, l.Status, l.Bytes)
			if (last != "" && l.TS < last) || printed[key] {
				continue
			}
			if l.TS != last {
				printed = map[string]bool{}
			}
			printed[key] = true
			if a.JSON {
				raw, _ := json.Marshal(l)
				fmt.Fprintln(a.Out, string(raw))
			} else {
				fmt.Fprintf(a.Out, "%s  %-6s %d  %s%s  %s  %s\n", l.TS, l.Method, l.Status, l.Host, l.Path, msText(&l.LatencyMS), bytesText(strconv.FormatInt(l.Bytes, 10)))
			}
			last = l.TS
		}
		if !follow {
			if logs.Truncated && !a.JSON {
				fmt.Fprintf(a.Err, "More requests than %d in this window: narrow it with --since, --status or --search.\n", limit)
			}
			return nil
		}
		if last != "" {
			from = last
		}
		select {
		case <-cmd.Context().Done():
			return nil
		case <-time.After(2 * time.Second):
		}
	}
}

// ── Cancel (`mw deployments cancel`) ───────────────────────────────────────

// The states a deployment can still be cancelled in: before its release.
var cancellable = map[string]bool{"WAITING_FOR_CI": true, "QUEUED": true, "BUILDING": true}

func deploymentsCancelCmd(a *App) *cobra.Command {
	var service, project string
	cmd := &cobra.Command{
		Use:   "cancel [<number>|<id>]",
		Short: "Cancel a deployment that has not reached its release (default: the one under way)",
		Long: "Cancel a deployment waiting for CI, queued or building; the revision live stays live.\n" +
			"Without an argument it is the service's deployment under way, when there is one. A\n" +
			"deployment running its release command is not interrupted half-way: roll back once it ends.",
		Example: "  mw deployments cancel\n  mw deployments cancel 14 --service api",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var target *api.Deployment
			name := ""
			if len(args) == 1 && uuidRe.MatchString(args[0]) {
				var d api.Deployment
				if err := c.Do(cmd.Context(), "GET", "/serverless/deployments/"+args[0], nil, nil, &d); err != nil {
					return err
				}
				target = &d
			} else {
				s, err := a.resolveService(cmd.Context(), service, project)
				if err != nil {
					return err
				}
				name = s.Name
				if target, err = a.deploymentToCancel(cmd.Context(), s, args); err != nil {
					return err
				}
			}
			var d api.Deployment
			if err := c.Do(cmd.Context(), "POST", "/serverless/deployments/"+target.ID+"/cancel", nil, nil, &d); err != nil {
				return err
			}
			of := ""
			if name != "" {
				of = " of " + name
			}
			return a.print(d, func(w *tabwriter.Writer) {
				if d.Status == "CANCELLED" {
					fmt.Fprintf(w, "Deployment #%d%s cancelled; what was live stays live.\n", d.Number, of)
				} else {
					fmt.Fprintf(w, "Deployment #%d%s is %s.\n", d.Number, of, strings.ToLower(strings.ReplaceAll(d.Status, "_", " ")))
				}
			})
		},
	}
	serviceFlag(cmd, &service, &project)
	return cmd
}

// deploymentToCancel is the deployment a number names, or else the one under
// way when there is exactly one.
func (a *App) deploymentToCancel(ctx context.Context, s *api.Service, args []string) (*api.Deployment, error) {
	c, _ := a.Client()
	var page api.Page[api.Deployment]
	if err := c.Do(ctx, "GET", "/serverless/services/"+s.ID+"/deployments", url.Values{"limit": {"50"}}, nil, &page); err != nil {
		return nil, err
	}
	if len(args) == 1 {
		want := strings.TrimPrefix(args[0], "#")
		for i := range page.Data {
			if strconv.Itoa(page.Data[i].Number) == want {
				return &page.Data[i], nil
			}
		}
		return nil, fmt.Errorf("%s has no deployment #%s among its last 50", s.Name, want)
	}
	var open, late []api.Deployment
	for _, d := range page.Data {
		switch {
		case cancellable[d.Status]:
			open = append(open, d)
		case !finished[d.Status]:
			late = append(late, d)
		}
	}
	switch {
	case len(open) == 1:
		return &open[0], nil
	case len(open) > 1:
		var nums []string
		for _, d := range open {
			nums = append(nums, "#"+strconv.Itoa(d.Number))
		}
		return nil, fmt.Errorf("%s has %d deployments that can be cancelled (%s): say which, e.g. `mw deployments cancel %d`", s.Name, len(open), strings.Join(nums, ", "), open[0].Number)
	case len(late) > 0:
		return nil, fmt.Errorf("deployment #%d of %s is %s: past the point it can be cancelled (roll back once it ends: `mw rollback`)", late[0].Number, s.Name, strings.ToLower(late[0].Status))
	default:
		return nil, fmt.Errorf("nothing to cancel: no deployment of %s is under way", s.Name)
	}
}

// ── Alerts ─────────────────────────────────────────────────────────────────

// alertKinds: what `mw alerts set` takes, as the API names them.
var alertKinds = map[string]string{
	"error-rate": "ERROR_RATE", "errors": "ERROR_RATE",
	"latency": "LATENCY_P95", "latency-p95": "LATENCY_P95", "p95": "LATENCY_P95",
	"memory":        "MEMORY",
	"max-instances": "MAX_INSTANCES",
}

func alertName(kind string) string {
	switch kind {
	case "ERROR_RATE":
		return "error-rate"
	case "LATENCY_P95":
		return "latency"
	case "MEMORY":
		return "memory"
	case "MAX_INSTANCES":
		return "max-instances"
	}
	return strings.ToLower(kind)
}

// thresholdText prints a threshold (or a measure) in its kind's unit.
func thresholdText(kind string, v float64) string {
	switch kind {
	case "LATENCY_P95":
		return msText(&v)
	case "MEMORY":
		return percent(v) + " of the limit"
	case "MAX_INSTANCES":
		return percent(v) + " of the maximum"
	default:
		return percent(v)
	}
}

// parseThreshold reads `5%`, `0.05`, `800ms` or `2s` for a kind.
func parseThreshold(kind, raw string) (float64, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if kind == "LATENCY_P95" {
		if d, err := time.ParseDuration(s); err == nil {
			return float64(d.Milliseconds()), nil
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || v <= 0 {
			return 0, fmt.Errorf("--threshold for latency is a time, like 800ms or 2s")
		}
		return v, nil
	}
	if strings.HasSuffix(s, "%") {
		v, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
		if err != nil {
			return 0, fmt.Errorf("--threshold %q is not a percentage", raw)
		}
		return v / 100, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("--threshold %q is not a number", raw)
	}
	if v > 1 {
		return 0, fmt.Errorf("--threshold %s: write a share as a percentage (%s%%) or a fraction (%s)", raw, raw, strconv.FormatFloat(v/100, 'f', -1, 64))
	}
	return v, nil
}

func alertsCmd(a *App) *cobra.Command {
	var service, project string
	cmd := &cobra.Command{
		Use:     "alerts",
		Aliases: []string{"alert"},
		Short:   "A service's alerts: what fires, when, and whether it fires now",
		Long: "Every web and private service has four alerts, on from the start: requests failing\n" +
			"(error-rate), slow responses (latency), memory near its limit (memory) and the maximum\n" +
			"instances reached (max-instances); workers and cron jobs have memory. They tell you in\n" +
			"the console, by e-mail and on your webhooks (`mw webhooks`).",
		Example: "  mw alerts\n  mw alerts set latency --threshold 1.5s --window 10\n  mw alerts set error-rate --off",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var rules []api.AlertRule
			if err := c.Do(cmd.Context(), "GET", "/serverless/services/"+s.ID+"/alerts", nil, nil, &rules); err != nil {
				return err
			}
			return a.print(rules, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "ALERT\tSTATE\tWHEN\tFOR\tE-MAIL\tLAST MEASURED")
				for _, r := range rules {
					state := strings.ToLower(r.State)
					if !r.Enabled {
						state = "off"
					} else if r.State == "FIRING" && r.Since != nil {
						state = "FIRING since " + when(*r.Since)
					}
					measured := "—"
					if r.LastValue != nil {
						measured = thresholdText(r.Kind, *r.LastValue)
					}
					fmt.Fprintf(w, "%s\t%s\tover %s\t%d min\t%s\t%s\n", alertName(r.Kind), state, thresholdText(r.Kind, r.Threshold), r.WindowMinutes, onOff(r.Email), measured)
				}
			})
		},
	}
	serviceFlag(cmd, &service, &project)

	var setService, setProject, threshold, window string
	var on, off, email bool
	set := &cobra.Command{
		Use:   "set <error-rate|latency|memory|max-instances>",
		Short: "Switch an alert on or off, or move its threshold and how long it must last",
		Example: "  mw alerts set memory --threshold 85%\n" +
			"  mw alerts set latency --threshold 800ms --window 15\n" +
			"  mw alerts set max-instances --off\n" +
			"  mw alerts set error-rate --on --email=false",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, ok := alertKinds[strings.ToLower(args[0])]
			if !ok {
				// The API's own names (ERROR_RATE…) are taken too.
				switch upper := strings.ToUpper(args[0]); upper {
				case "ERROR_RATE", "LATENCY_P95", "MEMORY", "MAX_INSTANCES":
					kind = upper
				default:
					return fmt.Errorf("no alert %q: it is error-rate, latency, memory or max-instances", args[0])
				}
			}
			if on && off {
				return errors.New("--on or --off, not both")
			}
			body := map[string]interface{}{}
			flags := cmd.Flags()
			if flags.Changed("threshold") {
				v, err := parseThreshold(kind, threshold)
				if err != nil {
					return err
				}
				body["threshold"] = v
			}
			if flags.Changed("window") {
				m, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(window), "min"), "m"))
				if err != nil || !map[int]bool{5: true, 10: true, 15: true, 30: true, 60: true}[m] {
					return errors.New("--window is 5, 10, 15, 30 or 60 (minutes)")
				}
				body["window_minutes"] = m
			}
			if on || off {
				body["enabled"] = on
			}
			if flags.Changed("email") {
				body["email"] = email
			}
			if len(body) == 0 {
				return errors.New("nothing to change: pass --threshold, --window, --on/--off or --email")
			}
			s, err := a.resolveService(cmd.Context(), setService, setProject)
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var r api.AlertRule
			if err := c.Do(cmd.Context(), "PATCH", "/serverless/services/"+s.ID+"/alerts/"+kind, nil, body, &r); err != nil {
				return err
			}
			return a.print(r, func(w *tabwriter.Writer) {
				if !r.Enabled {
					fmt.Fprintf(w, "%s: the %s alert is off.\n", s.Name, alertName(r.Kind))
					return
				}
				by := "in the console and on your webhooks"
				if r.Email {
					by = "in the console, by e-mail and on your webhooks"
				}
				fmt.Fprintf(w, "%s: the %s alert fires over %s for %d minutes, %s.\n", s.Name, alertName(r.Kind), thresholdText(r.Kind, r.Threshold), r.WindowMinutes, by)
			})
		},
	}
	serviceFlag(set, &setService, &setProject)
	set.Flags().StringVar(&threshold, "threshold", "", "when it fires: a share (5%, 0.9) or, for latency, a time (800ms, 2s)")
	set.Flags().StringVar(&window, "window", "", "how long it must last first, minutes: 5, 10, 15, 30 or 60")
	set.Flags().BoolVar(&on, "on", false, "switch it on")
	set.Flags().BoolVar(&off, "off", false, "switch it off")
	set.Flags().BoolVar(&email, "email", true, "also tell by e-mail (--email=false: not)")
	cmd.AddCommand(set)
	return cmd
}

// ── Egress ─────────────────────────────────────────────────────────────────

func egressCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "egress",
		Short: "What your services sent to visitors this period, of what it includes",
		Long: "Response bytes your services sent to visitors this period (1 GB = 10^9 bytes), against\n" +
			"the allowance the period includes, and what went past it costs. Files served from\n" +
			"Object Storage are not counted here.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var e api.Egress
			if err := c.Do(cmd.Context(), "GET", "/serverless/egress", nil, nil, &e); err != nil {
				return err
			}
			return a.print(e, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Period\t%s to %s\n", when(e.PeriodFrom), when(e.PeriodTo))
				fmt.Fprintf(w, "Sent to visitors\t%s\n", egressLine(&e))
				if e.PriceMicroCentsPerGB != nil {
					fmt.Fprintf(w, "Past the allowance\t%s per GB; %s so far\n", money(*e.PriceMicroCentsPerGB, 2), money(e.ChargedMicroCents, 2))
				}
				if len(e.Services) > 0 {
					fmt.Fprintln(w, "\nSERVICE\tSENT")
					for _, s := range e.Services {
						name := str(s.Name)
						if name == "" {
							name = "(deleted)"
						}
						fmt.Fprintf(w, "%s\t%s\n", name, bytesText(s.Bytes))
					}
				}
			})
		},
	}
}

// egressLine is "180.2 GB of 250.0 GB included (72%)".
func egressLine(e *api.Egress) string {
	line := bytesText(e.UsedBytes)
	used, _ := strconv.ParseFloat(e.UsedBytes, 64)
	allowance, _ := strconv.ParseFloat(e.AllowanceBytes, 64)
	if allowance > 0 {
		line += fmt.Sprintf(" of %s included (%s)", bytesText(e.AllowanceBytes), percent(math.Round(used/allowance*100)/100))
	}
	return line
}
