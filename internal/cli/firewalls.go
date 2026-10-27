package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// firewallsCmd: instance firewalls. Rules are staged; `apply` pushes them to
// the instances (and sets which instances the firewall protects).
func firewallsCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "firewalls",
		Aliases: []string{"firewall", "fw"},
		Short:   "Firewalls for instances: rules, and the instances they protect",
		Long: "Firewalls filter an instance's traffic. Rules you add, change or delete are staged:\n" +
			"`mw firewalls apply` pushes them to the instances it protects; `attach` and `detach`\n" +
			"change which instances those are (and apply at the same time).",
		Example: "  mw firewalls create web --description \"web servers\"\n" +
			"  mw firewalls rules add web --direction in --action ACCEPT --macro SSH --source 203.0.113.0/24\n" +
			"  mw firewalls rules add web --direction in --action ACCEPT --protocol tcp --port 80,443\n" +
			"  mw firewalls update web --policy-in DROP\n" +
			"  mw firewalls attach web web-1 --wait",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			all, err := a.firewalls(cmd.Context())
			if err != nil {
				return err
			}
			l := listing{headers: []string{"ID", "NAME", "ENABLED", "IN", "OUT", "RULES", "INSTANCES", "PENDING"}, empty: "No firewalls yet: `mw firewalls create <name>`."}
			for _, f := range all {
				pending := ""
				if f.HasPendingChanges {
					pending = "yes: `mw firewalls apply`"
				}
				l.add(f.ID, f.ID, f.Name, yesNo(f.Enabled), f.PolicyIn, f.PolicyOut, fmt.Sprint(len(f.Rules)), orDash(strings.Join(f.InstanceIDs, ",")), orDash(pending))
			}
			return a.printList(all, l)
		},
	}

	get := &cobra.Command{
		Use:   "get <firewall>",
		Short: "A firewall, its rules and the instances it protects",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := a.resolveFirewall(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.printFirewall(*f)
		},
	}

	var description string
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a firewall (empty, attached to nothing)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			body := map[string]interface{}{"name": args[0]}
			if description != "" {
				body["description"] = description
			}
			var f api.Firewall
			if err := c.Do(cmd.Context(), "POST", "/firewalls", nil, body, &f); err != nil {
				return err
			}
			if !a.JSON {
				fmt.Fprintf(a.Out, "Firewall %s created (%s). Add rules with `mw firewalls rules add %s …`.\n", f.Name, f.ID, f.Name)
				return nil
			}
			return a.encode(f)
		},
	}
	create.Flags().StringVar(&description, "description", "", "a note")

	var name, desc, policyIn, policyOut string
	var enable, disable bool
	update := &cobra.Command{
		Use:   "update <firewall>",
		Short: "Rename, switch on or off, or change the default policies",
		Long: "Change a firewall. The default policy applies to traffic no rule matches — DROP for\n" +
			"inbound cuts off everything you did not allow, your own SSH included, once applied.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := a.resolveFirewall(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			body := map[string]interface{}{}
			if cmd.Flags().Changed("name") {
				body["name"] = name
			}
			if cmd.Flags().Changed("description") {
				body["description"] = desc
			}
			if enable && disable {
				return errors.New("--enable or --disable, not both")
			}
			if enable || disable {
				body["enabled"] = enable
			}
			for flag, v := range map[string]string{"policy_in": policyIn, "policy_out": policyOut} {
				if v == "" {
					continue
				}
				p := strings.ToUpper(v)
				if p != "ACCEPT" && p != "DROP" && p != "REJECT" {
					return fmt.Errorf("--%s is ACCEPT, DROP or REJECT", strings.ReplaceAll(flag, "_", "-"))
				}
				body[flag] = p
			}
			if len(body) == 0 {
				return errors.New("nothing to change: pass --name, --description, --enable, --disable, --policy-in or --policy-out")
			}
			c, _ := a.Client()
			var out api.Firewall
			if err := c.Do(cmd.Context(), "PATCH", "/firewalls/"+f.ID, nil, body, &out); err != nil {
				return err
			}
			return a.printFirewall(out)
		},
	}
	update.Flags().StringVar(&name, "name", "", "new name")
	update.Flags().StringVar(&desc, "description", "", "a note")
	update.Flags().BoolVar(&enable, "enable", false, "switch it on")
	update.Flags().BoolVar(&disable, "disable", false, "switch it off (its instances are unfiltered)")
	update.Flags().StringVar(&policyIn, "policy-in", "", "default for inbound traffic: ACCEPT, DROP or REJECT")
	update.Flags().StringVar(&policyOut, "policy-out", "", "default for outbound traffic: ACCEPT, DROP or REJECT")

	var yes bool
	remove := &cobra.Command{
		Use:     "delete <firewall>",
		Aliases: []string{"remove", "rm"},
		Short:   "Delete a firewall: its instances are left unfiltered",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := a.resolveFirewall(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if !yes {
				ok, err := a.confirm(fmt.Sprintf("Delete firewall %s? Its %d instance(s) are left unfiltered. [y/N] ", f.Name, len(f.InstanceIDs)), "--yes")
				if err != nil || !ok {
					if err == nil {
						a.printf("Nothing deleted.\n")
					}
					return err
				}
			}
			c, _ := a.Client()
			if err := c.Do(cmd.Context(), "DELETE", "/firewalls/"+f.ID, nil, nil, nil); err != nil {
				return err
			}
			if a.JSON {
				return a.encode(map[string]interface{}{"id": f.ID, "deleted": true})
			}
			fmt.Fprintf(a.Out, "Firewall %s deleted.\n", f.Name)
			return nil
		},
	}
	remove.Flags().BoolVarP(&yes, "yes", "y", false, "delete without asking")

	macros := &cobra.Command{
		Use:   "macros",
		Short: "Named services a rule can allow (SSH, HTTP, MySQL…)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var all []api.FirewallMacro
			if err := c.Do(cmd.Context(), "GET", "/firewalls/macros", nil, nil, &all); err != nil {
				return err
			}
			l := listing{headers: []string{"MACRO", "DESCRIPTION"}}
			for _, m := range all {
				l.add(m.Name, m.Name, m.Description)
			}
			return a.printList(all, l)
		},
	}

	cmd.AddCommand(get, create, update, remove, macros, firewallRulesCmd(a),
		firewallApplyCmd(a, "apply"), firewallApplyCmd(a, "attach"), firewallApplyCmd(a, "detach"))
	return cmd
}

func (a *App) printFirewall(f api.Firewall) error {
	if a.JSON {
		return a.encode(f)
	}
	var d detail
	d.add("ID", f.ID)
	d.add("Name", f.Name)
	if f.Description != nil && *f.Description != "" {
		d.add("Description", *f.Description)
	}
	d.add("Enabled", yesNo(f.Enabled))
	d.add("Default in", f.PolicyIn)
	d.add("Default out", f.PolicyOut)
	d.add("Instances", orDash(strings.Join(f.InstanceIDs, ", ")))
	if f.HasPendingChanges {
		d.add("Pending", "changes not applied yet: `mw firewalls apply "+f.Name+"`")
	}
	if err := a.printDetail(f, d); err != nil {
		return err
	}
	fmt.Fprintln(a.Out)
	l := listing{headers: []string{"RULE", "#", "DIR", "ACTION", "WHAT", "SOURCE", "DESTINATION", "ON", "COMMENT"}, empty: "No rules yet."}
	for _, r := range f.Rules {
		l.add(r.ID, shortID(r.ID), fmt.Sprint(r.Position), r.Direction, r.Action, ruleWhat(r), orDashPtr(r.Source), orDashPtr(r.Destination), yesNo(r.Enabled), orDashPtr(r.Comment))
	}
	return a.printList(f.Rules, l)
}

func ruleWhat(r api.FirewallRule) string {
	if r.Macro != nil && *r.Macro != "" {
		return *r.Macro
	}
	what := str(r.Protocol)
	if r.DestPort != nil && *r.DestPort != "" {
		what += " " + *r.DestPort
	}
	if what == "" {
		return "any"
	}
	return strings.TrimSpace(what)
}

func (a *App) firewalls(ctx context.Context) ([]api.Firewall, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	return api.All[api.Firewall](ctx, c, "/firewalls", nil)
}

func (a *App) resolveFirewall(ctx context.Context, ref string) (*api.Firewall, error) {
	if uuidRe.MatchString(ref) {
		c, err := a.Client()
		if err != nil {
			return nil, err
		}
		var f api.Firewall
		if err := c.Do(ctx, "GET", "/firewalls/"+ref, nil, nil, &f); err != nil {
			return nil, err
		}
		return &f, nil
	}
	all, err := a.firewalls(ctx)
	if err != nil {
		return nil, err
	}
	for _, f := range all {
		if f.ID == ref || strings.EqualFold(f.Name, ref) || strings.HasPrefix(f.ID, ref) && len(ref) >= 8 {
			f := f
			return &f, nil
		}
	}
	return nil, fmt.Errorf("no firewall %s (`mw firewalls` lists them)", ref)
}

type ruleFlags struct {
	direction, action, macro, protocol, source, destination, sourcePort, port, comment string
	enabled, disabled                                                                  bool
}

func (r *ruleFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&r.direction, "direction", "", "in or out")
	f.StringVar(&r.action, "action", "", "ACCEPT, DROP or REJECT")
	f.StringVar(&r.macro, "macro", "", "a named service instead of protocol/ports (`mw firewalls macros`)")
	f.StringVar(&r.protocol, "protocol", "", "tcp, udp, icmp…")
	f.StringVar(&r.source, "source", "", "source address or CIDR (default: anywhere)")
	f.StringVar(&r.destination, "destination", "", "destination address or CIDR")
	f.StringVar(&r.sourcePort, "source-port", "", "source port or range")
	f.StringVar(&r.port, "port", "", "destination port, range (8000:8100) or list (80,443)")
	f.StringVar(&r.comment, "comment", "", "a note")
	f.BoolVar(&r.enabled, "enable", false, "switch the rule on")
	f.BoolVar(&r.disabled, "disable", false, "switch the rule off (kept, not applied)")
}

// body is the rule as the API takes it; only what was given.
func (r *ruleFlags) body(cmd *cobra.Command, creating bool) (map[string]interface{}, error) {
	b := map[string]interface{}{}
	if r.direction != "" {
		d := strings.ToLower(r.direction)
		if d != "in" && d != "out" {
			return nil, errors.New("--direction is in or out")
		}
		b["direction"] = d
	}
	if r.action != "" {
		act := strings.ToUpper(r.action)
		if act != "ACCEPT" && act != "DROP" && act != "REJECT" {
			return nil, errors.New("--action is ACCEPT, DROP or REJECT")
		}
		b["action"] = act
	}
	set := func(key, flag, v string) {
		if cmd.Flags().Changed(flag) {
			b[key] = v
		}
	}
	set("macro", "macro", r.macro)
	set("protocol", "protocol", strings.ToLower(r.protocol))
	set("source", "source", r.source)
	set("destination", "destination", r.destination)
	set("source_port", "source-port", r.sourcePort)
	set("dest_port", "port", r.port)
	set("comment", "comment", r.comment)
	if r.enabled && r.disabled {
		return nil, errors.New("--enable or --disable, not both")
	}
	if r.enabled || r.disabled {
		b["enabled"] = r.enabled
	}
	if creating {
		if b["direction"] == nil || b["action"] == nil {
			return nil, errors.New("a rule needs --direction (in/out) and --action (ACCEPT/DROP/REJECT)")
		}
		if r.macro != "" && (r.protocol != "" || r.port != "") {
			return nil, errors.New("--macro already says the protocol and ports: no --protocol or --port with it")
		}
	}
	if len(b) == 0 {
		return nil, errors.New("nothing to change")
	}
	return b, nil
}

func firewallRulesCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "A firewall's rules (staged until applied)",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	var addFlags ruleFlags
	add := &cobra.Command{
		Use:   "add <firewall>",
		Short: "Add a rule (staged: `mw firewalls apply` puts it in force)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := a.resolveFirewall(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			body, err := addFlags.body(cmd, true)
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var out api.Firewall
			if err := c.Do(cmd.Context(), "POST", "/firewalls/"+f.ID+"/rules", nil, body, &out); err != nil {
				return err
			}
			if a.JSON {
				return a.encode(out)
			}
			fmt.Fprintf(a.Out, "Rule added to %s (staged: `mw firewalls apply %s` puts it in force).\n", out.Name, out.Name)
			return nil
		},
	}
	addFlags.register(add)

	var updFlags ruleFlags
	update := &cobra.Command{
		Use:   "update <firewall> <rule-id>",
		Short: "Change a rule (staged)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := a.resolveFirewall(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			rule, err := findRule(f, args[1])
			if err != nil {
				return err
			}
			body, err := updFlags.body(cmd, false)
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var out api.Firewall
			if err := c.Do(cmd.Context(), "PATCH", "/firewalls/"+f.ID+"/rules/"+rule.ID, nil, body, &out); err != nil {
				return err
			}
			if a.JSON {
				return a.encode(out)
			}
			fmt.Fprintf(a.Out, "Rule %s changed (staged: `mw firewalls apply %s`).\n", shortID(rule.ID), out.Name)
			return nil
		},
	}
	updFlags.register(update)

	remove := &cobra.Command{
		Use:     "delete <firewall> <rule-id>",
		Aliases: []string{"remove", "rm"},
		Short:   "Delete a rule (staged)",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := a.resolveFirewall(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			rule, err := findRule(f, args[1])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			if err := c.Do(cmd.Context(), "DELETE", "/firewalls/"+f.ID+"/rules/"+rule.ID, nil, nil, nil); err != nil {
				return err
			}
			if a.JSON {
				return a.encode(map[string]interface{}{"id": rule.ID, "deleted": true})
			}
			fmt.Fprintf(a.Out, "Rule %s deleted (staged: `mw firewalls apply %s`).\n", shortID(rule.ID), f.Name)
			return nil
		},
	}
	cmd.AddCommand(add, update, remove)
	return cmd
}

// findRule takes a rule's id, the start of it, or its position.
func findRule(f *api.Firewall, ref string) (*api.FirewallRule, error) {
	for _, r := range f.Rules {
		if r.ID == ref || len(ref) >= 6 && strings.HasPrefix(r.ID, ref) || fmt.Sprint(r.Position) == ref {
			r := r
			return &r, nil
		}
	}
	return nil, fmt.Errorf("firewall %s has no rule %s (`mw firewalls get %s` lists them)", f.Name, ref, f.Name)
}

// firewallApplyCmd: apply (the staged rules, to the same instances), attach
// (add instances) and detach (remove them) — all one commit, whose
// instance list is the whole set the firewall protects.
func firewallApplyCmd(a *App, mode string) *cobra.Command {
	var instances []string
	var wait bool
	var timeout time.Duration
	use := map[string]string{
		"apply":  "apply <firewall>",
		"attach": "attach <firewall> <instance>...",
		"detach": "detach <firewall> <instance>...",
	}[mode]
	short := map[string]string{
		"apply":  "Put the staged rules in force on the instances it protects",
		"attach": "Protect these instances too (and apply the rules)",
		"detach": "Stop protecting these instances (they are left unfiltered)",
	}[mode]
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args: func(cmd *cobra.Command, args []string) error {
			if mode == "apply" {
				return cobra.ExactArgs(1)(cmd, args)
			}
			return cobra.MinimumNArgs(2)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			f, err := a.resolveFirewall(ctx, args[0])
			if err != nil {
				return err
			}
			set := map[string]bool{}
			for _, id := range f.InstanceIDs {
				set[id] = true
			}
			if mode == "apply" && len(instances) > 0 {
				set = map[string]bool{}
				for _, ref := range splitList(instances) {
					in, err := a.resolveInstance(ctx, ref)
					if err != nil {
						return err
					}
					set[in.ID] = true
				}
			}
			for _, ref := range args[1:] {
				in, err := a.resolveInstance(ctx, ref)
				if err != nil {
					return err
				}
				set[in.ID] = mode == "attach"
			}
			ids := []string{}
			for id, keep := range set {
				if keep {
					ids = append(ids, id)
				}
			}
			sortIDs(ids)
			c, _ := a.Client()
			var out api.FirewallCommit
			if err := c.Do(ctx, "POST", "/firewalls/"+f.ID+"/commit", nil, map[string]interface{}{"instance_ids": ids}, &out); err != nil {
				return err
			}
			if wait && out.OperationID != "" {
				if _, err := a.waitOperation(ctx, out.OperationID, timeout); err != nil {
					return err
				}
			}
			if a.JSON {
				return a.encode(out)
			}
			target := "no instances"
			if len(ids) > 0 {
				target = "instance(s) " + strings.Join(ids, ", ")
			}
			verb := "being applied"
			if wait {
				verb = "applied"
			}
			fmt.Fprintf(a.Out, "Firewall %s %s to %s", f.Name, verb, target)
			if !wait {
				fmt.Fprintf(a.Out, " (operation %s)", out.OperationID)
			}
			fmt.Fprintln(a.Out, ".")
			return nil
		},
	}
	if mode == "apply" {
		cmd.Flags().StringSliceVar(&instances, "instances", nil, "the whole set of instances it protects (default: the ones it does now)")
	}
	waitFlags(cmd, &wait, &timeout, 10*time.Minute)
	return cmd
}

// sortIDs sorts numeric ids numerically.
func sortIDs(ids []string) {
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && lessID(ids[j], ids[j-1]); j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
}

func lessID(x, y string) bool {
	if len(x) != len(y) {
		return len(x) < len(y)
	}
	return x < y
}
