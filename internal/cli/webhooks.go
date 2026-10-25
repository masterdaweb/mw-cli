package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// webhooksCmd: the account's webhooks — deployments, alerts, databases and
// billing events sent to your address (signed JSON), Slack or Discord.
func webhooksCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "webhooks",
		Aliases: []string{"webhook"},
		Short:   "Webhooks: events sent to your address, Slack or Discord",
		Long: "The account's webhooks. Your own address gets every event as JSON, signed with\n" +
			"`MW-Signature: t=…,v1=…` (HMAC-SHA256 of `<t>.<body>` with the webhook's secret, shown\n" +
			"once when it is made); a Slack or Discord address gets a line in the channel. A webhook\n" +
			"that keeps failing is switched off, and you are told.",
		Example: "  mw webhooks\n" +
			"  mw webhooks add https://loja.com.br/hooks/mw --events deployment.failed,alert.*\n" +
			"  mw webhooks add https://hooks.slack.com/services/T000/B000/XXXX\n" +
			"  mw webhooks test 0193f0e2\n" +
			"  mw webhooks remove 0193f0e2",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			hooks, err := a.webhooks(cmd.Context())
			if err != nil {
				return err
			}
			return a.print(hooks, func(w *tabwriter.Writer) {
				if len(hooks) == 0 {
					fmt.Fprintln(w, "No webhooks yet: `mw webhooks add <address>`.")
					return
				}
				fmt.Fprintln(w, "ID\tKIND\tADDRESS\tEVENTS\tSTATE\tLAST DELIVERED")
				for _, h := range hooks {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", shortID(h.ID), strings.ToLower(h.Kind), h.URL, eventsText(h.Events), webhookState(h), whenPtr(h.LastSuccessAt))
				}
			})
		},
	}

	var kind, description, locale string
	var events []string
	add := &cobra.Command{
		Use:   "add <address>",
		Short: "Add a webhook (its signing secret is shown this once)",
		Long: "Add a webhook. Slack and Discord addresses are recognised; any other https:// address\n" +
			"gets signed JSON. --events picks what it gets (none: every event; a family as alert.*):\n" +
			"`mw webhooks events` lists them.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			body := map[string]interface{}{"url": args[0]}
			k := strings.ToUpper(kind)
			if k == "" {
				k = webhookKindOf(args[0])
			}
			switch k {
			case "GENERIC", "SLACK", "DISCORD":
				body["kind"] = k
			default:
				return errors.New("--kind is generic, slack or discord")
			}
			if len(events) > 0 {
				body["events"] = splitList(events)
			}
			if description != "" {
				body["description"] = description
			}
			if locale != "" {
				body["locale"] = locale
			}
			var made api.CreatedWebhook
			if err := c.Do(cmd.Context(), "POST", "/webhooks", nil, body, &made); err != nil {
				return err
			}
			return a.print(made, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Webhook %s added (%s), for %s.\n", shortID(made.Webhook.ID), strings.ToLower(made.Webhook.Kind), eventsText(made.Webhook.Events))
				if made.Secret != nil {
					fmt.Fprintf(w, "\nSigning secret (shown this once — keep it where your receiver reads it):\n  %s\n", *made.Secret)
					fmt.Fprintln(w, "\nEach delivery carries `MW-Signature: t=<time>,v1=<HMAC-SHA256 of \"<time>.<body>\" with this secret>`.")
				}
				fmt.Fprintf(w, "Try it: `mw webhooks test %s`.\n", shortID(made.Webhook.ID))
			})
		},
	}
	add.Flags().StringVar(&kind, "kind", "", "generic, slack or discord (default: from the address)")
	add.Flags().StringSliceVar(&events, "events", nil, "what it gets, comma-separated (default: every event)")
	add.Flags().StringVar(&description, "description", "", "a note shown beside it")
	add.Flags().StringVar(&locale, "locale", "", "the language of a Slack or Discord message (e.g. pt-BR, en)")

	var updEvents []string
	var updDescription string
	var on, off, everyEvent bool
	update := &cobra.Command{
		Use:   "update <id>",
		Short: "Change what a webhook gets, or switch it on or off",
		Example: "  mw webhooks update 0193f0e2 --events deployment.*,alert.*\n" +
			"  mw webhooks update 0193f0e2 --off",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if on && off {
				return errors.New("--on or --off, not both")
			}
			body := map[string]interface{}{}
			if len(updEvents) > 0 && everyEvent {
				return errors.New("--events or --all-events, not both")
			}
			if len(updEvents) > 0 {
				body["events"] = splitList(updEvents)
			}
			if everyEvent {
				body["events"] = []string{}
			}
			if cmd.Flags().Changed("description") {
				body["description"] = updDescription
			}
			if on || off {
				body["enabled"] = on
			}
			if len(body) == 0 {
				return errors.New("nothing to change: pass --events, --all-events, --description, --on or --off")
			}
			h, err := a.webhook(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var out api.Webhook
			if err := c.Do(cmd.Context(), "PATCH", "/webhooks/"+h.ID, nil, body, &out); err != nil {
				return err
			}
			return a.print(out, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Webhook %s: %s, for %s.\n", shortID(out.ID), webhookState(out), eventsText(out.Events))
			})
		},
	}
	update.Flags().StringSliceVar(&updEvents, "events", nil, "what it gets, comma-separated")
	update.Flags().BoolVar(&everyEvent, "all-events", false, "every event")
	update.Flags().StringVar(&updDescription, "description", "", "a note shown beside it")
	update.Flags().BoolVar(&on, "on", false, "switch it on")
	update.Flags().BoolVar(&off, "off", false, "switch it off")

	var yesRemove bool
	remove := &cobra.Command{
		Use:     "remove <id>",
		Aliases: []string{"delete", "rm"},
		Short:   "Remove a webhook: deliveries stop at once",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := a.webhook(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if !yesRemove {
				ok, err := a.confirm(fmt.Sprintf("Remove the webhook to %s? Deliveries stop at once. [y/N] ", h.URL), "--yes")
				if err != nil || !ok {
					if err == nil {
						a.printf("Nothing removed.\n")
					}
					return err
				}
			}
			c, _ := a.Client()
			if err := c.Do(cmd.Context(), "DELETE", "/webhooks/"+h.ID, nil, nil, nil); err != nil {
				return err
			}
			return a.print(map[string]interface{}{"id": h.ID, "deleted": true}, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Webhook %s removed.\n", shortID(h.ID))
			})
		},
	}
	remove.Flags().BoolVarP(&yesRemove, "yes", "y", false, "remove without asking")

	var yesRotate bool
	rotate := &cobra.Command{
		Use:   "rotate <id>",
		Short: "A new signing secret (shown this once); the old one stops at once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := a.webhook(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if h.Kind != "GENERIC" {
				return fmt.Errorf("webhook %s is a %s one: only your own addresses are signed", shortID(h.ID), strings.ToLower(h.Kind))
			}
			if !yesRotate {
				ok, err := a.confirm("Your receiver rejects every delivery until it has the new secret. Rotate it? [y/N] ", "--yes")
				if err != nil || !ok {
					if err == nil {
						a.printf("The secret is unchanged.\n")
					}
					return err
				}
			}
			c, _ := a.Client()
			var out struct {
				Secret string `json:"secret"`
			}
			if err := c.Do(cmd.Context(), "POST", "/webhooks/"+h.ID+"/rotate-secret", nil, nil, &out); err != nil {
				return err
			}
			return a.print(out, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "New signing secret of %s (shown this once):\n  %s\n", shortID(h.ID), out.Secret)
			})
		},
	}
	rotate.Flags().BoolVarP(&yesRotate, "yes", "y", false, "rotate without asking")

	test := &cobra.Command{
		Use:   "test <id>",
		Short: "Send a webhook.test event now, and say what the address answered",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := a.webhook(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var r api.WebhookTest
			if err := c.Do(cmd.Context(), "POST", "/webhooks/"+h.ID+"/test", nil, nil, &r); err != nil {
				return err
			}
			if a.JSON {
				if err := a.print(r, nil); err != nil {
					return err
				}
			}
			if !r.OK {
				why := str(r.Error)
				if r.Status != nil {
					why = strings.TrimSpace(fmt.Sprintf("HTTP %d %s", *r.Status, why))
				}
				return fmt.Errorf("the test was not delivered to %s: %s", h.URL, why)
			}
			status := ""
			if r.Status != nil {
				status = fmt.Sprintf(" (HTTP %d)", *r.Status)
			}
			a.printf("Delivered to %s%s.\n", h.URL, status)
			return nil
		},
	}

	deliveries := &cobra.Command{
		Use:   "deliveries <id>",
		Short: "A webhook's last 50 deliveries, with what the address answered",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := a.webhook(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var ds []api.WebhookDelivery
			if err := c.Do(cmd.Context(), "GET", "/webhooks/"+h.ID+"/deliveries", nil, nil, &ds); err != nil {
				return err
			}
			return a.print(ds, func(w *tabwriter.Writer) {
				if len(ds) == 0 {
					fmt.Fprintln(w, "Nothing sent to it yet.")
					return
				}
				fmt.Fprintln(w, "EVENT\tSTATUS\tTRIES\tANSWER\tWHEN")
				for _, d := range ds {
					answer := str(d.LastError)
					if d.LastStatusCode != nil {
						answer = strings.TrimSpace(fmt.Sprintf("HTTP %d %s", *d.LastStatusCode, answer))
					}
					if answer == "" {
						answer = "—"
					}
					state := strings.ToLower(d.Status)
					if d.Status == "PENDING" && d.NextAttemptAt != nil {
						state = "retrying " + when(*d.NextAttemptAt)
					}
					fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", d.EventType, state, d.Attempts, answer, when(d.CreatedAt))
				}
			})
		},
	}

	eventTypes := &cobra.Command{
		Use:   "events",
		Short: "The events a webhook can get",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var out struct {
				Events []string `json:"events"`
			}
			if err := c.Do(cmd.Context(), "GET", "/webhooks/events", nil, nil, &out); err != nil {
				return err
			}
			return a.print(out, func(w *tabwriter.Writer) {
				for _, e := range out.Events {
					fmt.Fprintln(w, e)
				}
				fmt.Fprintln(w, "\nA family takes a star: alert.*, deployment.*, database.*, billing.*")
			})
		},
	}

	cmd.AddCommand(add, update, remove, rotate, test, deliveries, eventTypes)
	return cmd
}

func (a *App) webhooks(ctx context.Context) ([]api.Webhook, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	var hooks []api.Webhook
	if err := c.Do(ctx, "GET", "/webhooks", nil, nil, &hooks); err != nil {
		return nil, err
	}
	return hooks, nil
}

// webhook finds one by its id, the start of it (as the list shows it), or its
// description.
func (a *App) webhook(ctx context.Context, ref string) (*api.Webhook, error) {
	hooks, err := a.webhooks(ctx)
	if err != nil {
		return nil, err
	}
	var found []api.Webhook
	for _, h := range hooks {
		if h.ID == ref || (len(ref) >= 4 && strings.HasPrefix(h.ID, ref)) || (h.Description != nil && *h.Description == ref) {
			found = append(found, h)
		}
	}
	switch len(found) {
	case 1:
		return &found[0], nil
	case 0:
		return nil, fmt.Errorf("no webhook %s: `mw webhooks` lists them", ref)
	default:
		return nil, fmt.Errorf("%s names %d webhooks: give more of the id", ref, len(found))
	}
}

// webhookKindOf tells Slack and Discord addresses from your own.
func webhookKindOf(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return "GENERIC"
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "hooks.slack.com":
		return "SLACK"
	case (host == "discord.com" || host == "discordapp.com" || strings.HasSuffix(host, ".discord.com")) && strings.HasPrefix(u.Path, "/api/webhooks/"):
		return "DISCORD"
	}
	return "GENERIC"
}

func webhookState(h api.Webhook) string {
	if h.Enabled {
		if h.ConsecutiveFailures > 0 {
			return fmt.Sprintf("on (%d failures in a row)", h.ConsecutiveFailures)
		}
		return "on"
	}
	switch str(h.DisabledReason) {
	case "failing":
		return "off: it kept failing"
	case "removed":
		return "off: the address is gone"
	}
	return "off"
}

func eventsText(events []string) string {
	if len(events) == 0 {
		return "every event"
	}
	return strings.Join(events, ", ")
}

// splitList takes `a,b` and repeated flags alike.
func splitList(items []string) []string {
	var out []string
	for _, it := range items {
		for _, p := range strings.Split(it, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// confirm asks a yes/no question whose answer is no unless typed yes; with
// nothing to read (a script), it says which flag answers it instead.
func (a *App) confirm(question, flag string) (bool, error) {
	// With --json, standard output is the answer alone: ask on standard error.
	ask := a.Out
	if a.JSON {
		ask = a.Err
	}
	fmt.Fprint(ask, question)
	line, err := bufio.NewReader(a.In).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(ask)
			return false, fmt.Errorf("no answer: add %s to go ahead without being asked", flag)
		}
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "s", "sim":
		return true, nil
	default:
		return false, nil
	}
}
