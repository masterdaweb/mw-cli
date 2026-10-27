package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// followEvery is how often `mw activity --follow` asks for what is new.
var followEvery = 10 * time.Second

// activityFilter is GET /v1/activity's filters, as flags.
type activityFilter struct {
	action, category, actor, outcome, targetType, targetID, search, since, until string
}

var (
	activityCategories = []string{"instance", "network", "dns", "storage", "billing", "security", "account", "serverless"}
	activityActors     = []string{"CONSOLE", "API_TOKEN", "MCP", "SUPPORT", "SYSTEM", "SUPPORT_BOT"}
	activityOutcomes   = []string{"success", "failure", "denied"}
)

func activityCmd(a *App) *cobra.Command {
	var f activityFilter
	var limit int
	var follow bool
	cmd := &cobra.Command{
		Use:     "activity",
		Aliases: []string{"audit"},
		Short:   "Who did what in the account, from where, and how it went (the audit log)",
		Long: "The account's activity log: each change, by whom — a person in the console, an API token,\n" +
			"an AI assistant, support or the platform — from where, and whether it worked. Newest first;\n" +
			"--follow keeps printing what happens, oldest first, the way a log is read.",
		Example: "  mw activity\n" +
			"  mw activity --category instance --since 24h\n" +
			"  mw activity --target-type instance --target-id 24377\n" +
			"  mw activity --outcome denied --actor api-token --since 7d\n" +
			"  mw activity --follow",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q, err := f.query(time.Now())
			if err != nil {
				return err
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			entries, err := activityPage(cmd.Context(), c, q, limit)
			if err != nil {
				return notOffered(err, "The activity log from the API")
			}
			if follow {
				return a.followActivity(cmd.Context(), c, q, entries)
			}
			l := listing{headers: []string{"TIME", "ACTOR", "ACTION", "TARGET", "OUTCOME"}, empty: "Nothing in the activity log for that."}
			for _, e := range entries {
				l.add(e.ID, when(e.CreatedAt), actorText(e.Actor), e.Action, targetText(e.Target), outcomeText(e))
			}
			return a.printList(entries, l)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.action, "action", "", "one action, like instance.power.reboot")
	fl.StringVar(&f.category, "category", "", strings.Join(activityCategories, ", "))
	fl.StringVar(&f.actor, "actor", "", "who acted: console, api-token, mcp (an AI assistant), support, system or support-bot")
	fl.StringVar(&f.outcome, "outcome", "", "success, failure or denied")
	fl.StringVar(&f.targetType, "target-type", "", "what it was done to: instance, vpc, dns_zone, …")
	fl.StringVar(&f.targetID, "target-id", "", "the id of what it was done to")
	fl.StringVar(&f.search, "search", "", "text in the action, the actor or the target")
	fl.StringVar(&f.since, "since", "", "from when: a duration back (30m, 6h, 7d) or a date (2026-10-01, 2026-10-01T15:04)")
	fl.StringVar(&f.until, "until", "", "up to when, the same way")
	fl.IntVar(&limit, "limit", 50, "how many entries (0: all)")
	fl.BoolVarP(&follow, "follow", "f", false, "keep printing new entries, every 10 seconds, until interrupted")
	return cmd
}

// query is the filters as the API takes them, each checked first.
func (f activityFilter) query(now time.Time) (url.Values, error) {
	q := url.Values{}
	set := func(key, value string) {
		if value != "" {
			q.Set(key, value)
		}
	}
	set("action", f.action)
	set("target_type", f.targetType)
	set("target_id", f.targetID)
	set("search", f.search)
	if f.category != "" {
		c := strings.ToLower(f.category)
		if !containsFold(activityCategories, c) {
			return nil, fmt.Errorf("--category is one of %s", strings.Join(activityCategories, ", "))
		}
		q.Set("category", c)
	}
	if f.actor != "" {
		k := strings.ToUpper(strings.ReplaceAll(f.actor, "-", "_"))
		if !containsFold(activityActors, k) {
			return nil, errors.New("--actor is console, api-token, mcp, support, system or support-bot")
		}
		q.Set("actor_kind", k)
	}
	if f.outcome != "" {
		o := strings.ToLower(f.outcome)
		if !containsFold(activityOutcomes, o) {
			return nil, errors.New("--outcome is success, failure or denied")
		}
		q.Set("outcome", o)
	}
	for key, value := range map[string]string{"from": f.since, "to": f.until} {
		if value == "" {
			continue
		}
		t, err := parseWhen(value, now)
		if err != nil {
			return nil, err
		}
		q.Set(key, t.UTC().Format(time.RFC3339))
	}
	return q, nil
}

// parseWhen reads a moment: a duration back from now (30m, 6h, 7d), a date
// (2026-10-01, at midnight here) or a date and time.
func parseWhen(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if days, ok := strings.CutSuffix(s, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n >= 0 {
			return now.Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a moment: a duration back (30m, 6h, 7d) or a date (2026-10-01, 2026-10-01T15:04)", s)
}

// activityPage is the newest n entries (0: all) the filters let through.
func activityPage(ctx context.Context, c *api.Client, filters url.Values, n int) ([]api.ActivityEntry, error) {
	q := url.Values{}
	for k, v := range filters {
		q[k] = v
	}
	size := n
	if size <= 0 || size > 100 {
		size = 100
	}
	q.Set("limit", strconv.Itoa(size))
	var out []api.ActivityEntry
	for {
		var p api.Page[api.ActivityEntry]
		if err := c.Do(ctx, "GET", "/activity", q, nil, &p); err != nil {
			return nil, err
		}
		out = append(out, p.Data...)
		if (n > 0 && len(out) >= n) || !p.HasMore || p.NextCursor == "" {
			break
		}
		q.Set("cursor", p.NextCursor)
	}
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// followActivity prints the entries it was given, then what arrives, every
// followEvery, until interrupted — oldest first, the way a log is read.
func (a *App) followActivity(ctx context.Context, c *api.Client, filters url.Values, first []api.ActivityEntry) error {
	seen := map[string]bool{}
	print := func(newestFirst []api.ActivityEntry) error {
		for i := len(newestFirst) - 1; i >= 0; i-- {
			e := newestFirst[i]
			if seen[e.ID] {
				continue
			}
			seen[e.ID] = true
			if err := a.printActivityLine(e); err != nil {
				return err
			}
		}
		return nil
	}
	if !a.JSON && !a.Quiet && !a.NoHeaders {
		fmt.Fprintf(a.Out, activityLine, "TIME", "ACTOR", "ACTION", "TARGET", "OUTCOME")
	}
	if err := print(first); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(followEvery):
		}
		// Pages until one it has seen, so a busy minute is not cut short.
		var batch []api.ActivityEntry
		q := url.Values{}
		for k, v := range filters {
			q[k] = v
		}
		q.Set("limit", "100")
		var err error
		for pages := 0; pages < 5; pages++ {
			var p api.Page[api.ActivityEntry]
			if err = c.Do(ctx, "GET", "/activity", q, nil, &p); err != nil {
				break
			}
			batch = append(batch, p.Data...)
			old := len(p.Data) == 0
			for _, e := range p.Data {
				old = old || seen[e.ID]
			}
			if old || !p.HasMore || p.NextCursor == "" {
				break
			}
			q.Set("cursor", p.NextCursor)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// A token that stopped working ends it; a blip does not.
			var p *api.Problem
			if errors.Is(err, api.ErrUnauthorized) || (errors.As(err, &p) && (p.Status == http.StatusUnauthorized || p.Status == http.StatusForbidden)) {
				return err
			}
			fmt.Fprintf(a.Err, "(could not read the activity log: %s — trying again)\n", Describe(err))
			continue
		}
		if err := print(batch); err != nil {
			return err
		}
	}
}

const activityLine = "%-16s  %-28s  %-30s  %-28s  %s\n"

// printActivityLine is one entry as --follow prints it: a row, one JSON
// document per line, or its id.
func (a *App) printActivityLine(e api.ActivityEntry) error {
	switch {
	case a.JSON && a.Output == outputYAML:
		if err := writeYAML(a.Out, e); err != nil {
			return err
		}
		_, err := fmt.Fprintln(a.Out, "---")
		return err
	case a.JSON:
		return json.NewEncoder(a.Out).Encode(e)
	case a.Quiet:
		_, err := fmt.Fprintln(a.Out, e.ID)
		return err
	}
	_, err := fmt.Fprintf(a.Out, activityLine, when(e.CreatedAt), clip(actorText(e.Actor), 28), clip(e.Action, 30), clip(targetText(e.Target), 28), outcomeText(e))
	return err
}

var actorKinds = map[string]string{
	"CONSOLE": "console", "API_TOKEN": "API token", "MCP": "AI assistant",
	"SUPPORT": "support", "SYSTEM": "platform", "SUPPORT_BOT": "support bot",
}

// actorText is who acted: "ana@example.com (console)", "deploy (API token)".
func actorText(ac api.ActivityActor) string {
	kind := firstNonEmpty(actorKinds[ac.Kind], strings.ToLower(ac.Kind))
	switch {
	case ac.Label == "":
		return orDash(kind)
	case kind == "" || strings.EqualFold(ac.Label, kind):
		return ac.Label
	}
	return ac.Label + " (" + kind + ")"
}

// targetText is what it was done to: "instance web-1", "vpc 12".
func targetText(t api.ActivityTarget) string {
	what := firstNonEmpty(str(t.Label), str(t.ID))
	if typ := str(t.Type); typ != "" {
		return strings.TrimSpace(strings.ReplaceAll(typ, "_", " ") + " " + what)
	}
	return orDash(what)
}

// outcomeText is how it went, with why when it failed.
func outcomeText(e api.ActivityEntry) string {
	if e.Outcome != "success" && e.ErrorMessage != nil && *e.ErrorMessage != "" {
		return e.Outcome + ": " + clip(*e.ErrorMessage, 60)
	}
	return e.Outcome
}

// clip shortens s to n characters, with an ellipsis.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
