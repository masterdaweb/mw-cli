package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// The Security Center from the terminal: the score, the findings that lower
// it (riskiest first), the risks the account accepted, and each instance's
// scans — AWS Security Hub's findings and suppressions, for this platform.

var (
	securitySeverities    = []string{"critical", "high", "medium", "low", "info"}
	securityStatuses      = []string{"open", "resolved", "excepted", "false_positive"}
	securityResourceTypes = []string{"instance", "serverless_service", "database", "bucket", "storage_key", "account"}
	securityCategories    = []string{"EXPOSED_SERVICE", "AMPLIFICATION_VECTOR", "VULNERABLE_SOFTWARE", "WEAK_ENCRYPTION",
		"CERTIFICATE", "MISCONFIGURATION", "INFORMATION_DISCLOSURE", "DEFAULT_CREDENTIALS", "ACCESS_CONTROL", "NETWORK_POLICY"}
	scanFrequencies = []string{"weekly", "monthly"}
)

// The justification an accepted risk needs, as the API checks it.
const (
	minJustification = 30
	maxJustification = 2000
)

func securityCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "security",
		Aliases: []string{"security-center", "sec"},
		Short:   "Security Center: the account's score, its findings and the risks you accepted",
		Long: "The account's security score (0–100) and what lowers it: open findings on instances,\n" +
			"serverless services, databases, buckets, access keys and the account itself, riskiest first.\n" +
			"Instances are scanned from outside, as an attacker sees them; the rest is checked from how\n" +
			"it is set up. A risk you decide to live with is accepted with a justification and an end\n" +
			"date, and stops lowering the score until then.",
		Example: "  mw security\n" +
			"  mw security findings --severity high\n" +
			"  mw security findings --instance web-1\n" +
			"  mw security finding 0192f5c4-3b7a-7cc1-9a51-6b1f9c4d2e10\n" +
			"  mw security accept-risk --port 3306 --instance db-1 --justification \"…\" --until 90d\n" +
			"  mw instances scan web-1 --wait",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.securityOverview(cmd.Context()) },
	}
	overview := &cobra.Command{
		Use:   "overview",
		Short: "The score, open findings by severity, the engines behind the score and the worst resources",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return a.securityOverview(cmd.Context()) },
	}
	finding := &cobra.Command{
		Use:   "finding <id>",
		Short: "One finding: why it matters, what to do, references and its timeline",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return a.securityFinding(cmd.Context(), args[0]) },
	}
	cmd.AddCommand(overview, securityFindingsCmd(a), finding, securityExceptionsCmd(a),
		securityAcceptRiskCmd(a), securityRevokeCmd(a), securitySettingsCmd(a))
	return cmd
}

func (a *App) securityOverview(ctx context.Context) error {
	c, err := a.Client()
	if err != nil {
		return err
	}
	var o api.SecurityOverview
	if err := c.Do(ctx, "GET", "/security/overview", nil, nil, &o); err != nil {
		return notOffered(err, "The Security Center from the API")
	}
	if a.JSON {
		return a.encode(o)
	}
	var d detail
	d.add("Score", scoreText(o.Score, o.Grade, "not checked yet"))
	d.add("Last check", whenPtr(o.LastScanAt))
	d.add("Open findings", countsText(o.OpenFindings))
	in := o.Instances
	parts := []string{fmt.Sprintf("%d assessed", in.Assessed)}
	if in.AwaitingFirstScan > 0 {
		parts = append(parts, fmt.Sprintf("%d awaiting their first scan", in.AwaitingFirstScan))
	}
	if in.Excluded > 0 {
		parts = append(parts, strings.TrimSpace(fmt.Sprintf("%d not scanned %s", in.Excluded, parens(reasonsText(in.ExcludedReasons)))))
	}
	if in.OptedOut > 0 {
		parts = append(parts, fmt.Sprintf("%d with scanning off", in.OptedOut))
	}
	d.add("Instances", fmt.Sprintf("%d: %s", in.Total, strings.Join(parts, ", ")))
	for _, r := range o.Resources {
		if r.Total == 0 {
			continue
		}
		line := fmt.Sprintf("%d, %d checked", r.Total, r.Checked)
		if len(r.ImageScans) > 0 {
			line += "; images: " + reasonsText(r.ImageScans)
		}
		d.add(resourceTypeText(r.Type, r.Total != 1), line)
	}
	if len(o.Engines) > 0 {
		d.after = append(d.after, "")
		d.after = append(d.after, engineTable(o.Engines)...)
	}
	worst := append(append([]api.SecurityResourceScore{}, o.WorstInstances...), o.WorstResources...)
	if len(worst) > 0 {
		var b strings.Builder
		w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "WORST\tSCORE\tCRITICAL\tHIGH")
		for _, r := range worst {
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\n", resourceLabel(r.Type, r.ID, str(r.Name)), scoreText(r.Score, r.Grade, "—"), r.OpenCritical, r.OpenHigh)
		}
		_ = w.Flush()
		d.after = append(d.after, "")
		d.after = append(d.after, strings.Split(strings.TrimRight(b.String(), "\n"), "\n")...)
	}
	if !o.ScanningAvailable {
		d.after = append(d.after, "", "Scanning is paused on the platform: the score is not being refreshed for now.")
	}
	if total := o.OpenFindings.Critical + o.OpenFindings.High + o.OpenFindings.Medium + o.OpenFindings.Low + o.OpenFindings.Info; total > 0 {
		d.after = append(d.after, "", "`mw security findings` lists them, riskiest first.")
	}
	return a.printDetail(o, d)
}

// securityFilter is GET /v1/security/findings's filters, as flags.
type securityFilter struct {
	severity, status, resourceType, resourceID, instance, category, search string
}

func securityFindingsCmd(a *App) *cobra.Command {
	var f securityFilter
	var limit int
	cmd := &cobra.Command{
		Use:     "findings",
		Aliases: []string{"finding-list"},
		Short:   "Findings, riskiest first (by default only what still counts: not resolved, not accepted)",
		Example: "  mw security findings\n" +
			"  mw security findings --severity critical\n" +
			"  mw security findings --instance web-1 --status resolved\n" +
			"  mw security findings --resource-type bucket\n" +
			"  mw security findings --category VULNERABLE_SOFTWARE --limit 0",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			q, err := a.securityQuery(ctx, f)
			if err != nil {
				return err
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			all, err := api.Take[api.SecurityFinding](ctx, c, "/security/findings", q, limit)
			if err != nil {
				return notOffered(err, "The Security Center from the API")
			}
			l := listing{headers: []string{"ID", "SEVERITY", "RISK", "RESOURCE", "FINDING", "LAST SEEN"},
				empty: "No findings for that. `mw security` shows the score."}
			for _, x := range all {
				l.add(x.ID, x.ID, severityText(x), fnum(x.RiskScore), resourceLabel(x.Resource.Type, x.Resource.ID, x.Resource.Name),
					cell(findingTitle(x), 70), when(x.LastSeenAt))
			}
			return a.printList(all, l)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.severity, "severity", "", strings.Join(securitySeverities, ", "))
	fl.StringVar(&f.status, "status", "", "open, resolved, excepted (accepted) or false_positive (default: what still counts)")
	fl.StringVar(&f.resourceType, "resource-type", "", strings.Join(securityResourceTypes, ", "))
	fl.StringVar(&f.resourceID, "resource-id", "", "with --resource-type (not instance): that resource only")
	fl.StringVar(&f.instance, "instance", "", "one instance's findings (id or name)")
	fl.StringVar(&f.category, "category", "", "EXPOSED_SERVICE, VULNERABLE_SOFTWARE, WEAK_ENCRYPTION, CERTIFICATE, …")
	fl.StringVar(&f.search, "search", "", "text in the title, the check, the address or the resource name")
	fl.IntVar(&limit, "limit", 50, "how many findings (0: all)")
	completeWith(cmd, "severity", securitySeverities)
	completeWith(cmd, "status", securityStatuses)
	completeWith(cmd, "resource-type", securityResourceTypes)
	completeWith(cmd, "category", securityCategories)

	get := &cobra.Command{
		Use:   "get <id>",
		Short: "One finding (the same as `mw security finding <id>`)",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return a.securityFinding(cmd.Context(), args[0]) },
	}
	cmd.AddCommand(get)
	return cmd
}

// securityQuery checks the filters and puts them as the API takes them.
func (a *App) securityQuery(ctx context.Context, f securityFilter) (url.Values, error) {
	q := url.Values{}
	if f.severity != "" {
		s := strings.ToLower(f.severity)
		if !containsFold(securitySeverities, s) {
			return nil, fmt.Errorf("--severity is one of %s", strings.Join(securitySeverities, ", "))
		}
		q.Set("severity", s)
	}
	if f.status != "" {
		s := strings.ToLower(strings.ReplaceAll(f.status, "-", "_"))
		if s == "accepted" {
			s = "excepted"
		}
		if !containsFold(securityStatuses, s) {
			return nil, errors.New("--status is open, resolved, excepted (accepted) or false_positive")
		}
		q.Set("status", s)
	}
	if f.resourceType != "" {
		t := strings.ToLower(strings.ReplaceAll(f.resourceType, "-", "_"))
		if !containsFold(securityResourceTypes, t) {
			return nil, fmt.Errorf("--resource-type is one of %s", strings.Join(securityResourceTypes, ", "))
		}
		q.Set("resource_type", t)
	}
	if f.resourceID != "" {
		if t := q.Get("resource_type"); t == "" || t == "instance" {
			return nil, errors.New("--resource-id goes with a --resource-type other than instance; for an instance use --instance")
		}
		q.Set("resource_id", f.resourceID)
	}
	if f.instance != "" {
		in, err := a.resolveInstance(ctx, f.instance)
		if err != nil {
			return nil, err
		}
		q.Set("instance_id", in.ID)
	}
	if f.category != "" {
		c := strings.ToUpper(strings.ReplaceAll(f.category, "-", "_"))
		if !containsFold(securityCategories, c) {
			return nil, fmt.Errorf("--category is one of %s", strings.Join(securityCategories, ", "))
		}
		q.Set("category", c)
	}
	if s := strings.TrimSpace(f.search); s != "" {
		q.Set("search", s)
	}
	return q, nil
}

func (a *App) securityFinding(ctx context.Context, id string) error {
	if !uuidRe.MatchString(strings.ToLower(id)) {
		return fmt.Errorf("%s is not a finding id: `mw security findings` lists them", id)
	}
	c, err := a.Client()
	if err != nil {
		return err
	}
	var x api.SecurityFinding
	if err := c.Do(ctx, "GET", "/security/findings/"+strings.ToLower(id), nil, nil, &x); err != nil {
		return notOffered(err, "The Security Center from the API")
	}
	if a.JSON {
		return a.encode(x)
	}
	var d detail
	d.add("ID", x.ID)
	d.add("Finding", findingTitle(x))
	d.add("Severity", severityLongText(x))
	d.add("Status", findingStatusText(x.Status))
	d.add("Risk score", fnum(x.RiskScore))
	d.add("Resource", resourceLabel(x.Resource.Type, x.Resource.ID, x.Resource.Name))
	d.add("Check", x.RuleID)
	d.add("Category", x.Category)
	if x.Port != nil {
		d.add("Port", portText(*x.Port, str(x.Protocol)))
	}
	if len(x.CVEIDs) > 0 {
		d.add("CVEs", strings.Join(x.CVEIDs, ", "))
	}
	d.add("First seen", when(x.FirstSeenAt))
	d.add("Last seen", when(x.LastSeenAt))
	if x.LastVerifiedAt != nil {
		d.add("Last confirmed", whenPtr(x.LastVerifiedAt))
	}
	if x.ResolvedAt != nil {
		d.add("Resolved", whenPtr(x.ResolvedAt))
	}
	if x.OccurrenceCount > 1 {
		d.add("Seen", strconv.Itoa(x.OccurrenceCount)+" times")
	}
	section := func(title, text string) {
		if t := strings.TrimSpace(text); t != "" {
			d.after = append(d.after, "", title)
			for _, line := range strings.Split(t, "\n") {
				d.after = append(d.after, "  "+line)
			}
		}
	}
	section("What was found", x.Description)
	section("Why it matters", x.Impact)
	section("What to do", x.Recommendation)
	if len(x.References) > 0 {
		d.after = append(d.after, "", "References")
		for _, r := range x.References {
			d.after = append(d.after, "  "+r)
		}
	}
	if len(x.Events) > 0 {
		d.after = append(d.after, "", "Timeline")
		for _, e := range x.Events {
			d.after = append(d.after, fmt.Sprintf("  %s  %s", when(e.OccurredAt), strings.ReplaceAll(e.Type, "_", " ")))
		}
	}
	if x.Status == "open" {
		d.after = append(d.after, "", fmt.Sprintf("Fixed it? `mw instances scan <instance> --wait` checks again. Living with it: `mw security accept-risk --finding %s --justification \"…\" --until 90d`.", x.ID))
	}
	return a.printDetail(x, d)
}

func securityExceptionsCmd(a *App) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:     "exceptions",
		Aliases: []string{"accepted-risks", "exception"},
		Short:   "The risks the account accepted (--all: expired and revoked ones too)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			q := url.Values{}
			if all {
				q.Set("include_inactive", "true")
			}
			var list api.List[api.SecurityException]
			if err := c.Do(cmd.Context(), "GET", "/security/exceptions", q, nil, &list); err != nil {
				return notOffered(err, "The Security Center from the API")
			}
			l := listing{headers: []string{"ID", "SCOPE", "COVERS", "INSTANCE", "STATUS", "UNTIL", "WHY"},
				empty: "No accepted risks: every finding counts. `mw security accept-risk --help` says how to accept one."}
			for _, e := range list.Data {
				until := when(e.ExpiresAt)
				if e.RevokedAt != nil {
					until = "revoked " + whenPtr(e.RevokedAt)
				}
				l.add(e.ID, e.ID, e.Scope, exceptionCovers(e), orDashPtr(e.InstanceID), e.Status, until, cell(e.Justification, 50))
			}
			if list.Data == nil {
				list.Data = []api.SecurityException{}
			}
			return a.printList(list.Data, l)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "also the expired and revoked ones")
	return cmd
}

func securityAcceptRiskCmd(a *App) *cobra.Command {
	var finding, rule, port, protocol, instance, justification, until string
	cmd := &cobra.Command{
		Use:   "accept-risk",
		Short: "Accept a risk: a finding, a check or a port stops lowering the score until a date",
		Long: "Accept a risk you decided to live with, with why (kept for the audit) and until when —\n" +
			"the platform caps how long. Exactly one of:\n\n" +
			"  --finding <id>     that one finding\n" +
			"  --rule <check>     a check (the finding's \"Check\"), everywhere or on one --instance\n" +
			"  --port <n>[/udp]   a port, everywhere or on one --instance\n\n" +
			"Its findings stop counting at once; `mw security revoke <exception>` brings them back.",
		Example: "  mw security accept-risk --finding 0192f5c4-3b7a-7cc1-9a51-6b1f9c4d2e10 \\\n" +
			"    --justification \"Reachable only from the analytics vendor's fixed address.\" --until 90d\n" +
			"  mw security accept-risk --port 3306 --instance db-1 --justification \"…\" --until 2026-12-31",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			body := map[string]interface{}{}
			set := 0
			for _, v := range []string{finding, rule, port} {
				if v != "" {
					set++
				}
			}
			if set != 1 {
				return errors.New("say what to accept: exactly one of --finding, --rule or --port")
			}
			switch {
			case finding != "":
				if !uuidRe.MatchString(strings.ToLower(finding)) {
					return fmt.Errorf("--finding %s is not a finding id: `mw security findings` lists them", finding)
				}
				if instance != "" {
					return errors.New("--instance goes with --rule or --port: a finding names its own resource")
				}
				body["scope"] = "finding"
				body["finding_id"] = strings.ToLower(finding)
			case rule != "":
				body["scope"] = "rule"
				body["rule_id"] = rule
			default:
				n, proto, err := parsePortProto(port, protocol)
				if err != nil {
					return err
				}
				body["scope"] = "port"
				body["port"] = n
				body["protocol"] = proto
			}
			if instance != "" {
				in, err := a.resolveInstance(ctx, instance)
				if err != nil {
					return err
				}
				body["instance_id"] = in.ID
			}
			why := strings.TrimSpace(justification)
			if n := utf8.RuneCountInString(why); n < minJustification || n > maxJustification {
				return fmt.Errorf("--justification says why the risk is accepted, in %d to %d characters (it has %d)", minJustification, maxJustification, n)
			}
			body["justification"] = why
			if until == "" {
				return errors.New("--until says when it lapses: a date (2026-12-31) or a time from now (90d, 12w)")
			}
			end, err := parseUntil(until, time.Now())
			if err != nil {
				return err
			}
			body["expires_at"] = end.UTC().Format(time.RFC3339)
			c, err := a.Client()
			if err != nil {
				return err
			}
			var e api.SecurityException
			if err := c.Do(ctx, "POST", "/security/exceptions", nil, body, &e); err != nil {
				return notOffered(err, "Accepting risks from the API")
			}
			if a.JSON {
				return a.encode(e)
			}
			fmt.Fprintf(a.Out, "Risk accepted until %s (exception %s): %s no longer lowers the score.\n", when(e.ExpiresAt), e.ID, exceptionCovers(e))
			if got, err := time.Parse(time.RFC3339Nano, e.ExpiresAt); err == nil && got.Before(end.Add(-time.Minute)) {
				fmt.Fprintf(a.Out, "The platform's maximum applies: until %s, not %s.\n", when(e.ExpiresAt), end.Local().Format("2006-01-02 15:04"))
			}
			fmt.Fprintf(a.Out, "`mw security revoke %s` ends it sooner.\n", e.ID)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&finding, "finding", "", "a finding id")
	fl.StringVar(&rule, "rule", "", "a check id (a finding's \"Check\")")
	fl.StringVar(&port, "port", "", "a port: 3306, or 53/udp")
	fl.StringVar(&protocol, "protocol", "", "with --port: tcp (default) or udp")
	fl.StringVar(&instance, "instance", "", "with --rule or --port: one instance only (default: everywhere)")
	fl.StringVar(&justification, "justification", "", fmt.Sprintf("why the risk is accepted, kept for the audit (%d–%d characters)", minJustification, maxJustification))
	fl.StringVar(&until, "until", "", "when it lapses: a date (2026-12-31) or a time from now (90d, 12w, 720h)")
	completeWith(cmd, "protocol", []string{"tcp", "udp"})
	return cmd
}

func securityRevokeCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <exception>",
		Short: "Revoke an accepted risk: its findings count again at once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.ToLower(args[0])
			if !uuidRe.MatchString(id) {
				return fmt.Errorf("%s is not an exception id: `mw security exceptions` lists them", args[0])
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			if err := c.Do(cmd.Context(), "DELETE", "/security/exceptions/"+id, nil, nil, nil); err != nil {
				return notOffered(err, "Accepting risks from the API")
			}
			if a.JSON {
				return a.encode(map[string]interface{}{"id": id, "revoked": true})
			}
			fmt.Fprintf(a.Out, "Exception %s revoked: its findings count against the score again.\n", id)
			return nil
		},
	}
}

func securitySettingsCmd(a *App) *cobra.Command {
	show := func(cmd *cobra.Command, _ []string) error {
		c, err := a.Client()
		if err != nil {
			return err
		}
		var s api.SecuritySettings
		if err := c.Do(cmd.Context(), "GET", "/security/settings", nil, nil, &s); err != nil {
			return notOffered(err, "The Security Center from the API")
		}
		return a.printDetail(s, securitySettingsDetail(s))
	}
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "Scanning and notifications for the account",
		Args:  cobra.NoArgs,
		RunE:  show,
	}
	get := &cobra.Command{Use: "get", Short: "The account's scanning and notification settings", Args: cobra.NoArgs, RunE: show}

	var scanning, frequency, notifyCritical, notifyDrop string
	set := &cobra.Command{
		Use:   "set",
		Short: "Turn scanning on or off, change how often instances are checked, or who is told",
		Example: "  mw security settings set --frequency monthly\n" +
			"  mw security settings set --notify-score-drop off\n" +
			"  mw security settings set --scanning off",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body := map[string]interface{}{}
			for flag, key := range map[string]string{"scanning": "enabled", "notify-critical": "notify_on_new_critical", "notify-score-drop": "notify_on_score_drop"} {
				if !cmd.Flags().Changed(flag) {
					continue
				}
				v, _ := cmd.Flags().GetString(flag)
				on, err := parseSwitch("--"+flag, v)
				if err != nil {
					return err
				}
				body[key] = on
			}
			if frequency != "" {
				fr := strings.ToLower(frequency)
				if !containsFold(scanFrequencies, fr) {
					return errors.New("--frequency is weekly or monthly")
				}
				body["default_frequency"] = fr
			}
			if len(body) == 0 {
				return errors.New("nothing to change: pass --scanning, --frequency, --notify-critical or --notify-score-drop")
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			var s api.SecuritySettings
			if err := c.Do(cmd.Context(), "PATCH", "/security/settings", nil, body, &s); err != nil {
				return notOffered(err, "The Security Center from the API")
			}
			d := securitySettingsDetail(s)
			if _, ok := body["default_frequency"]; ok {
				d.after = append(d.after, "", "Every instance is now checked "+s.DefaultFrequency+".")
			}
			return a.printDetail(s, d)
		},
	}
	fl := set.Flags()
	fl.StringVar(&scanning, "scanning", "", "on or off: scan the account's instances at all")
	fl.StringVar(&frequency, "frequency", "", "how often instances are checked: weekly or monthly (applies to every instance)")
	fl.StringVar(&notifyCritical, "notify-critical", "", "on or off: email when a critical finding appears")
	fl.StringVar(&notifyDrop, "notify-score-drop", "", "on or off: email when the score drops")
	completeWith(set, "frequency", scanFrequencies)
	for _, f := range []string{"scanning", "notify-critical", "notify-score-drop"} {
		completeWith(set, f, []string{"on", "off"})
	}
	cmd.AddCommand(get, set)
	return cmd
}

func securitySettingsDetail(s api.SecuritySettings) detail {
	var d detail
	d.add("Scanning", onOff(s.Enabled))
	d.add("Frequency", orDash(s.DefaultFrequency))
	d.add("Email on a new critical", onOff(s.NotifyOnNewCritical))
	d.add("Email when the score drops", onOff(s.NotifyOnScoreDrop))
	return d
}

// Per instance: `mw instances security|scan|scans`.

func instanceSecurityCmd(a *App) *cobra.Command {
	var enable, disable bool
	var frequency string
	cmd := &cobra.Command{
		Use:   "security <instance>",
		Short: "An instance's security posture; --disable / --enable / --frequency change its scanning",
		Example: "  mw instances security web-1\n" +
			"  mw instances security web-1 --frequency monthly\n" +
			"  mw instances security lab-3 --disable",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if enable && disable {
				return errors.New("--enable or --disable, not both")
			}
			in, err := a.resolveInstance(ctx, args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			body := map[string]interface{}{}
			if enable || disable {
				body["enabled"] = enable
			}
			if frequency != "" {
				fr := strings.ToLower(frequency)
				if !containsFold(scanFrequencies, fr) {
					return errors.New("--frequency is weekly or monthly")
				}
				body["schedule_frequency"] = fr
			}
			var p api.InstanceSecurity
			if len(body) > 0 {
				err = c.Do(ctx, "PATCH", "/instances/"+in.ID+"/security", nil, body, &p)
			} else {
				err = c.Do(ctx, "GET", "/instances/"+in.ID+"/security", nil, nil, &p)
			}
			if err != nil {
				return notOffered(err, "Instance security from the API")
			}
			return a.printDetail(p, instanceSecurityDetail(in, p))
		},
	}
	cmd.Flags().BoolVar(&enable, "enable", false, "scan this instance (the default)")
	cmd.Flags().BoolVar(&disable, "disable", false, "stop scanning this instance")
	cmd.Flags().StringVar(&frequency, "frequency", "", "how often it is checked: weekly or monthly")
	completeWith(cmd, "frequency", scanFrequencies)
	return cmd
}

func instanceSecurityDetail(in *api.Instance, p api.InstanceSecurity) detail {
	var d detail
	d.add("Instance", instanceLabel(in))
	d.add("State", postureStateText(p))
	d.add("Score", scoreText(p.Score, p.Grade, "—"))
	d.add("Open findings", countsText(p.OpenFindings))
	d.add("Last scan", whenPtr(p.LastScanAt))
	d.add("Next scan", whenPtr(p.NextScanDueAt))
	d.add("Frequency", orDashPtr(p.ScheduleFrequency))
	if len(p.Engines) > 0 {
		d.after = append(d.after, "")
		d.after = append(d.after, engineTable(p.Engines)...)
	}
	switch p.State {
	case "opted_out":
		d.after = append(d.after, "", fmt.Sprintf("Scanning is off for this instance: `mw instances security %s --enable` turns it back on.", in.ID))
	case "account_opted_out":
		d.after = append(d.after, "", "Scanning is off for the account: `mw security settings set --scanning on` turns it back on.")
	case "assessed":
		if n := p.OpenFindings.Critical + p.OpenFindings.High + p.OpenFindings.Medium + p.OpenFindings.Low + p.OpenFindings.Info; n > 0 {
			d.after = append(d.after, "", fmt.Sprintf("`mw security findings --instance %s` lists them; `mw instances scan %s --wait` checks again after a fix.", in.ID, in.ID))
		}
	}
	if !p.ScanningAvailable {
		d.after = append(d.after, "", "Scanning is paused on the platform for now.")
	}
	return d
}

func postureStateText(p api.InstanceSecurity) string {
	switch p.State {
	case "assessed":
		return "assessed"
	case "not_yet_scanned":
		return "not scanned yet"
	case "excluded":
		return strings.TrimSpace("not scanned " + parens(strings.ReplaceAll(str(p.ExcludedReason), "_", " ")))
	case "opted_out":
		return "scanning off for this instance"
	case "account_opted_out":
		return "scanning off for the account"
	}
	return orDash(p.State)
}

func instanceScanCmd(a *App) *cobra.Command {
	var wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "scan <instance>",
		Short: "Check an instance's security now, from outside (it must be running)",
		Long: "Start a security scan of a running instance from outside, as an attacker would see it —\n" +
			"after a fix, to confirm it took. One at a time and a few a day per instance; --wait\n" +
			"follows it to the end (minutes).",
		Example: "  mw instances scan web-1 --wait",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			in, err := a.resolveInstance(ctx, args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var s api.SecurityScan
			if err := c.Do(ctx, "POST", "/instances/"+in.ID+"/security/scans", nil, nil, &s); err != nil {
				return notOffered(err, "Security scans from the API")
			}
			if wait {
				end, err := a.waitScan(ctx, in, s.ID, timeout)
				if end != nil {
					s = *end
				}
				if err != nil {
					return err
				}
			}
			if a.JSON {
				return a.encode(s)
			}
			if !wait || !s.Terminal() {
				fmt.Fprintf(a.Out, "Scan %s of %s %s: `mw instances scans %s` follows it (or add --wait).\n", s.ID, instanceLabel(in), firstNonEmpty(s.Status, "queued"), in.ID)
				return nil
			}
			fmt.Fprintln(a.Out, scanSummary(in, s))
			return nil
		},
	}
	waitFlags(cmd, &wait, &timeout, 30*time.Minute)
	return cmd
}

// waitScan follows a scan through the instance's scan list until it ends.
func (a *App) waitScan(ctx context.Context, in *api.Instance, scanID string, timeout time.Duration) (*api.SecurityScan, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	last := ""
	for {
		var list api.List[api.SecurityScan]
		err := c.Do(ctx, "GET", "/instances/"+in.ID+"/security/scans", url.Values{"limit": {"10"}}, nil, &list)
		if err != nil && ctx.Err() == nil {
			return nil, err
		}
		for _, s := range list.Data {
			if s.ID != scanID {
				continue
			}
			if s.Status != last && !a.JSON {
				fmt.Fprintf(a.Err, "  scan %s: %s\n", s.ID, s.Status)
				last = s.Status
			}
			if s.Terminal() {
				s := s
				if s.Status == "failed" || s.Status == "cancelled" {
					return &s, fmt.Errorf("the scan of %s %s: `mw instances scans %s` lists it", instanceLabel(in), s.Status, in.ID)
				}
				return &s, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("the scan of %s has not ended after %s: `mw instances scans %s` says where it is", instanceLabel(in), timeout, in.ID)
		case <-time.After(pollEvery):
		}
	}
}

func scanSummary(in *api.Instance, s api.SecurityScan) string {
	what := "done"
	switch s.Status {
	case "partial":
		what = "done, though some checks did not finish"
	case "skipped":
		return fmt.Sprintf("The scan of %s was skipped: nothing was due to be checked.", instanceLabel(in))
	}
	line := fmt.Sprintf("Scan of %s %s: score %s; %s, %d resolved, %d reopened.", instanceLabel(in), what,
		scoreText(s.Score, s.Grade, "—"), plural(s.FindingsNew, "new finding", "new findings"), s.FindingsResolved, s.FindingsReopened)
	if s.FindingsNew > 0 || s.FindingsReopened > 0 {
		line += fmt.Sprintf("\n`mw security findings --instance %s` lists them.", in.ID)
	}
	return line
}

func instanceScansCmd(a *App) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "scans <instance>",
		Short: "An instance's security scans, newest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			in, err := a.resolveInstance(ctx, args[0])
			if err != nil {
				return err
			}
			if limit < 1 || limit > 100 {
				return errors.New("--limit is 1 to 100")
			}
			c, _ := a.Client()
			var list api.List[api.SecurityScan]
			if err := c.Do(ctx, "GET", "/instances/"+in.ID+"/security/scans", url.Values{"limit": {strconv.Itoa(limit)}}, nil, &list); err != nil {
				return notOffered(err, "Security scans from the API")
			}
			l := listing{headers: []string{"ID", "STATUS", "TRIGGER", "QUEUED", "FINISHED", "SCORE", "NEW", "RESOLVED", "REOPENED"},
				empty: fmt.Sprintf("No scans of %s yet: `mw instances scan %s` starts one.", instanceLabel(in), in.ID)}
			for _, s := range list.Data {
				l.add(s.ID, s.ID, s.Status, strings.ReplaceAll(s.Trigger, "_", "-"), when(s.QueuedAt), whenPtr(s.FinishedAt),
					scoreText(s.Score, s.Grade, "—"), strconv.Itoa(s.FindingsNew), strconv.Itoa(s.FindingsResolved), strconv.Itoa(s.FindingsReopened))
			}
			if list.Data == nil {
				list.Data = []api.SecurityScan{}
			}
			return a.printList(list.Data, l)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "how many scans (up to 100)")
	return cmd
}

// Formatting.

// scoreText is "72/100 (C)", or none when there is no score.
func scoreText(score *float64, grade *string, none string) string {
	if score == nil {
		return none
	}
	s := fnum(*score) + "/100"
	if g := str(grade); g != "" {
		s += " (" + g + ")"
	}
	return s
}

func countsText(c api.SeverityCounts) string {
	return fmt.Sprintf("%d critical · %d high · %d medium · %d low · %d info", c.Critical, c.High, c.Medium, c.Low, c.Info)
}

var securityEngineNames = map[string]string{
	"nmap":    "exposure (open ports)",
	"sslyze":  "TLS",
	"nuclei":  "web checks",
	"openvas": "known vulnerabilities",
	"posture": "configuration",
	"image":   "container images",
}

// engineTable is the parts of a score, one per line.
func engineTable(engines []api.SecurityEngine) []string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ENGINE\tSCORE\tLAST CHECK\tCRITICAL\tHIGH\tMEDIUM\tLOW")
	for _, e := range engines {
		name := firstNonEmpty(securityEngineNames[e.Engine], e.Engine)
		score := scoreText(e.Score, e.Grade, "not run yet")
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%d\n", name, score, whenPtr(e.LastScanAt),
			e.OpenFindings.Critical, e.OpenFindings.High, e.OpenFindings.Medium, e.OpenFindings.Low)
	}
	_ = w.Flush()
	return strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
}

var resourceTypeNames = map[string][2]string{
	"instance":           {"instance", "Instances"},
	"serverless_service": {"service", "Services"},
	"database":           {"database", "Databases"},
	"bucket":             {"bucket", "Buckets"},
	"storage_key":        {"access key", "Access keys"},
	"account":            {"account", "Account"},
}

// resourceTypeText is a kind of resource as a heading ("Buckets").
func resourceTypeText(t string, many bool) string {
	n, ok := resourceTypeNames[t]
	if !ok {
		return strings.ReplaceAll(t, "_", " ")
	}
	if many || t == "account" {
		return n[1]
	}
	return strings.ToUpper(n[0][:1]) + n[0][1:]
}

// resourceLabel is "instance web-1 (42)", "bucket assets".
func resourceLabel(t, id, name string) string {
	kind := strings.ReplaceAll(t, "_", " ")
	if n, ok := resourceTypeNames[t]; ok {
		kind = n[0]
	}
	switch {
	case t == "account":
		return "the account"
	case name != "" && name != id && t == "instance":
		return fmt.Sprintf("%s %s (%s)", kind, name, id)
	case name != "":
		return kind + " " + name
	}
	return kind + " " + id
}

func severityText(x api.SecurityFinding) string {
	if x.KnownExploited {
		return x.Severity + " (exploited)"
	}
	return x.Severity
}

func severityLongText(x api.SecurityFinding) string {
	if x.KnownExploited {
		return x.Severity + ": actively exploited in the wild (CISA KEV)"
	}
	return x.Severity
}

func findingStatusText(s string) string {
	switch s {
	case "excepted":
		return "accepted (an exception covers it)"
	case "false_positive":
		return "false positive"
	}
	return s
}

func findingTitle(x api.SecurityFinding) string {
	return firstNonEmpty(strings.TrimSpace(x.Title), x.RuleID)
}

// exceptionCovers is what an exception silences, said briefly.
func exceptionCovers(e api.SecurityException) string {
	switch e.Scope {
	case "rule":
		return "check " + str(e.RuleID)
	case "port":
		if e.Port != nil {
			return portText(*e.Port, str(e.Protocol))
		}
	case "finding":
		if e.Resource != nil {
			return "a finding on " + resourceLabel(e.Resource.Type, e.Resource.ID, "")
		}
		return "a finding"
	}
	return e.Scope
}

func portText(port int, proto string) string {
	return strings.ToUpper(firstNonEmpty(proto, "tcp")) + " " + strconv.Itoa(port)
}

// reasonsText is a tally as "no public ip: 2, opted out: 1".
func reasonsText(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %d", strings.ReplaceAll(k, "_", " "), m[k]))
	}
	return strings.Join(parts, ", ")
}

// cell is text as one line of a table cell, at most n characters.
func cell(s string, n int) string {
	return clip(strings.Join(strings.Fields(s), " "), n)
}

// parsePortProto reads 3306, 53/udp or 443/tcp, with --protocol for the
// bare number.
func parsePortProto(raw, protocol string) (int, string, error) {
	num, proto, hasProto := strings.Cut(strings.ToLower(strings.TrimSpace(raw)), "/")
	if !hasProto {
		proto = strings.ToLower(strings.TrimSpace(protocol))
	} else if protocol != "" && !strings.EqualFold(protocol, proto) {
		return 0, "", fmt.Errorf("--port %s and --protocol %s disagree", raw, protocol)
	}
	if proto == "" {
		proto = "tcp"
	}
	if proto != "tcp" && proto != "udp" {
		return 0, "", errors.New("the protocol is tcp or udp")
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 1 || n > 65535 {
		return 0, "", fmt.Errorf("--port %s: a port is 1 to 65535", raw)
	}
	return n, proto, nil
}

// parseUntil reads a moment ahead: a time from now (90d, 12w, 720h) or a
// date (2026-12-31, the end of that day here) or a date and time.
func parseUntil(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	for suffix, unit := range map[string]time.Duration{"d": 24 * time.Hour, "w": 7 * 24 * time.Hour} {
		if n, ok := strings.CutSuffix(s, suffix); ok {
			if v, err := strconv.Atoi(n); err == nil && v > 0 {
				return now.Add(time.Duration(v) * unit), nil
			}
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return time.Time{}, fmt.Errorf("--until %s is not ahead of now", s)
		}
		return now.Add(d), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		t = t.Add(24*time.Hour - time.Second)
		if !t.After(now) {
			return time.Time{}, fmt.Errorf("--until %s is in the past", s)
		}
		return t, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			if !t.After(now) {
				return time.Time{}, fmt.Errorf("--until %s is in the past", s)
			}
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("--until %q is not a moment ahead: a date (2026-12-31) or a time from now (90d, 12w, 720h)", s)
}

// parseSwitch reads on/off (yes/no, true/false, enable/disable).
func parseSwitch(flag, v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "yes", "true", "enable", "enabled", "1":
		return true, nil
	case "off", "no", "false", "disable", "disabled", "0":
		return false, nil
	}
	return false, fmt.Errorf("%s is on or off (not %q)", flag, v)
}

// completeWith offers a flag's fixed values to shell completion.
func completeWith(cmd *cobra.Command, flag string, values []string) {
	_ = cmd.RegisterFlagCompletionFunc(flag, cobra.FixedCompletions(values, cobra.ShellCompDirectiveNoFileComp))
}
