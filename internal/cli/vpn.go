package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// VPN gateways: an instance running the platform's VPN appliance — remote
// access for people (the VPN app, a username and password each) and
// site-to-site tunnels. The gateway is ordered like any instance (the VPN
// gateway image); this is where it is run from. Users and settings change the
// record first and the gateway on apply: several edits, one reconfiguration.

// vpnUsernameRe is what the API takes as a login name.
var vpnUsernameRe = regexp.MustCompile(`^[a-zA-Z0-9]{1,64}$`)

const rebuildRequired = "VPN_GATEWAY_REBUILD_REQUIRED"

func vpnCmd(a *App) *cobra.Command {
	list := func(cmd *cobra.Command, _ []string) error {
		c, err := a.Client()
		if err != nil {
			return err
		}
		all, err := api.All[api.VPNGateway](cmd.Context(), c, "/vpn-gateways", nil)
		if err != nil {
			return notOffered(err, "VPN gateways from the API")
		}
		l := listing{headers: []string{"ID", "NAME", "LOCATION", "PUBLIC IP", "STATE", "USERS", "SITES", "CHANGES"},
			empty: "No VPN gateways: order one like any instance, with the VPN gateway image (`mw images`)."}
		for _, g := range all {
			l.add(g.ID, g.ID, orDashPtr(g.Name), orDashPtr(g.Location), orDashPtr(g.PublicIP), gatewayState(g),
				strconv.Itoa(g.UsersCount), strconv.Itoa(g.SiteConnectionsCount), pendingText(g))
		}
		if all == nil {
			all = []api.VPNGateway{}
		}
		return a.printList(all, l)
	}
	cmd := &cobra.Command{
		Use:     "vpn",
		Aliases: []string{"vpn-gateways", "vpn-gateway", "vpns"},
		Short:   "VPN gateways: who can connect, settings, applying changes, the profile people import",
		Long: "VPN gateways: remote access for people (the OpenVPN app, a username and password each) and\n" +
			"site-to-site tunnels to offices and other clouds. A gateway is an instance ordered with the\n" +
			"VPN gateway image. Users and settings are saved first and put in place by `mw vpn apply`\n" +
			"(or --apply on the change): several edits, one reconfiguration.",
		Example: "  mw vpn\n" +
			"  mw vpn users create office maria --email maria@loja.com.br --send-credentials --apply\n" +
			"  mw vpn profile office office.ovpn\n" +
			"  mw vpn update office --pool 10.9.0.0/24\n" +
			"  mw vpn apply office --wait",
		Args: cobra.NoArgs,
		RunE: list,
	}
	ls := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "The account's VPN gateways", Args: cobra.NoArgs, RunE: list}
	get := &cobra.Command{
		Use:   "get <gateway>",
		Short: "A gateway: its address pool, transport, users, sites and whether changes wait to be applied",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := a.resolveVPN(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.printDetail(g, gatewayDetail(*g))
		},
	}
	cmd.AddCommand(ls, get, vpnUpdateCmd(a), vpnApplyCmd(a), vpnProfileCmd(a), vpnUsersCmd(a), vpnSitesCmd(a))
	return cmd
}

func vpnUpdateCmd(a *App) *cobra.Command {
	var name, protocol, pool, allTraffic, shared string
	var mtu int
	var apply bool
	cmd := &cobra.Command{
		Use:   "update <gateway>",
		Short: "Change a gateway's settings (put in place by `mw vpn apply`, or --apply)",
		Long: "Change a gateway's settings. Renaming is immediate; the rest is saved and put in place by\n" +
			"`mw vpn apply` (or --apply). Sending all traffic through the gateway, or not, needs a\n" +
			"rebuild when applied: a few minutes offline.",
		Example: "  mw vpn update office --pool 10.9.0.0/24 --apply\n" +
			"  mw vpn update office --protocol tcp --mtu 1380\n" +
			"  mw vpn update office --route-all-traffic on",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			body := map[string]interface{}{}
			fl := cmd.Flags()
			if fl.Changed("name") {
				if strings.TrimSpace(name) == "" {
					return errors.New("--name cannot be empty")
				}
				body["name"] = strings.TrimSpace(name)
			}
			if fl.Changed("protocol") {
				p := strings.ToLower(protocol)
				if p != "udp" && p != "tcp" {
					return errors.New("--protocol is udp or tcp")
				}
				body["protocol"] = p
			}
			if fl.Changed("pool") {
				ip, network, err := net.ParseCIDR(pool)
				if err != nil || ip.To4() == nil {
					return fmt.Errorf("--pool %s is not an IPv4 network like 10.8.0.0/24", pool)
				}
				body["remote_access_pool"] = network.String()
			}
			if fl.Changed("route-all-traffic") {
				on, err := parseSwitch("--route-all-traffic", allTraffic)
				if err != nil {
					return err
				}
				body["route_all_traffic"] = on
			}
			if fl.Changed("shared-logins") {
				on, err := parseSwitch("--shared-logins", shared)
				if err != nil {
					return err
				}
				body["allow_shared_logins"] = on
			}
			if fl.Changed("mtu") {
				if mtu < 576 || mtu > 1500 {
					return errors.New("--mtu is 576 to 1500")
				}
				body["mtu"] = mtu
			}
			if len(body) == 0 {
				return errors.New("nothing to change: pass --name, --protocol, --pool, --route-all-traffic, --shared-logins or --mtu")
			}
			g, err := a.resolveVPN(ctx, args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var out api.VPNGateway
			if err := c.Do(ctx, "PATCH", "/vpn-gateways/"+g.ID, nil, body, &out); err != nil {
				return notOffered(err, "VPN gateways from the API")
			}
			if apply && out.PendingChanges {
				return a.applyAfterChange(ctx, &out, func() error { return a.printDetail(out, gatewayDetail(out)) })
			}
			return a.printDetail(out, gatewayDetail(out))
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&name, "name", "", "rename the gateway (its instance)")
	fl.StringVar(&protocol, "protocol", "", "transport of the VPN app's connections: udp or tcp")
	fl.StringVar(&pool, "pool", "", "the addresses people connecting get, as an IPv4 network (e.g. 10.8.0.0/24)")
	fl.StringVar(&allTraffic, "route-all-traffic", "", "on: all of a connected person's traffic goes through the gateway; off: only the private network's")
	fl.StringVar(&shared, "shared-logins", "", "on: one login may be connected from several devices at once")
	fl.IntVar(&mtu, "mtu", 0, "the tunnel's MTU, 576 to 1500")
	fl.BoolVar(&apply, "apply", false, "put the change in place right away (in place only; a rebuild stays `mw vpn apply --rebuild`)")
	completeWith(cmd, "protocol", []string{"udp", "tcp"})
	completeWith(cmd, "route-all-traffic", []string{"on", "off"})
	completeWith(cmd, "shared-logins", []string{"on", "off"})
	return cmd
}

func vpnApplyCmd(a *App) *cobra.Command {
	var rebuild, yes, wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "apply <gateway>",
		Short: "Put a gateway's pending changes in place",
		Long: "Put the users and settings changed since the last apply in place. Usually the running\n" +
			"gateway is reconfigured in seconds. Some changes need a rebuild — a few minutes offline,\n" +
			"everyone connected disconnected: then this stops and asks for --rebuild.",
		Example: "  mw vpn apply office --wait\n  mw vpn apply office --rebuild --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			g, err := a.resolveVPN(ctx, args[0])
			if err != nil {
				return err
			}
			if rebuild && !yes {
				ok, err := a.confirm(fmt.Sprintf("Rebuild %s now? The VPN goes offline for a few minutes and everyone connected is disconnected. [y/N] ", vpnRef(*g)), "--yes")
				if err != nil || !ok {
					if err == nil {
						a.printf("Nothing applied.\n")
					}
					return err
				}
			}
			out, err := a.vpnApply(ctx, g, rebuild)
			if err != nil {
				return err
			}
			if wait {
				if err := a.waitVPNApply(ctx, g, out, timeout); err != nil {
					return err
				}
			}
			if a.JSON {
				return a.encode(out)
			}
			fmt.Fprintln(a.Out, applyText(*g, *out, wait))
			return nil
		},
	}
	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "go ahead when the changes need a rebuild (or rebuild anyway): a few minutes offline")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "with --rebuild: rebuild without asking")
	waitFlags(cmd, &wait, &timeout, 20*time.Minute)
	return cmd
}

// vpnApply asks for an apply, saying plainly when it needs a rebuild.
func (a *App) vpnApply(ctx context.Context, g *api.VPNGateway, rebuild bool) (*api.VPNApply, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	body := map[string]interface{}{}
	if rebuild {
		body["rebuild"] = true
	}
	var out api.VPNApply
	if err := c.Do(ctx, "POST", "/vpn-gateways/"+g.ID+"/apply", nil, body, &out); err != nil {
		if errors.Is(err, api.Code(rebuildRequired)) {
			return nil, fmt.Errorf("applying these changes rebuilds %s: the VPN goes offline for a few minutes and everyone connected is disconnected.\n"+
				"  Run `mw vpn apply %s --rebuild` to go ahead", vpnRef(*g), vpnRef(*g))
		}
		return nil, notOffered(err, "Applying VPN changes from the API")
	}
	return &out, nil
}

// applyAfterChange applies a change just saved (--apply), after printing it.
func (a *App) applyAfterChange(ctx context.Context, g *api.VPNGateway, printed func() error) error {
	if err := printed(); err != nil {
		return err
	}
	out, err := a.vpnApply(ctx, g, false)
	if err != nil {
		return fmt.Errorf("saved, but not applied: %w", err)
	}
	a.printf("%s\n", applyText(*g, *out, false))
	return nil
}

// waitVPNApply follows an apply: its operation, or the gateway's own state.
func (a *App) waitVPNApply(ctx context.Context, g *api.VPNGateway, out *api.VPNApply, timeout time.Duration) error {
	if out.OperationID != nil && *out.OperationID != "" {
		_, err := a.waitOperation(ctx, *out.OperationID, timeout)
		return err
	}
	c, err := a.Client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		var now api.VPNGateway
		err := c.Do(ctx, "GET", "/vpn-gateways/"+g.ID, nil, nil, &now)
		if err == nil {
			switch now.ApplyState {
			case "failed":
				return fmt.Errorf("applying the changes to %s failed %s: `mw vpn get %s` says more", vpnRef(*g), firstNonEmpty(parens(str(now.LastApplyErrorCode)), "(no reason given)"), vpnRef(*g))
			case "idle":
				if !now.PendingChanges {
					return nil
				}
			}
		} else if ctx.Err() == nil {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s is still applying after %s: `mw vpn get %s` says where it is", vpnRef(*g), timeout, vpnRef(*g))
		case <-time.After(pollEvery):
		}
	}
}

func applyText(g api.VPNGateway, out api.VPNApply, waited bool) string {
	op := ""
	if out.OperationID != nil && *out.OperationID != "" {
		op = fmt.Sprintf(" (operation %s — `mw operations wait %s`)", *out.OperationID, *out.OperationID)
	}
	switch {
	case waited && out.Mode == "rebuild":
		return fmt.Sprintf("%s rebuilt with the changes in place.", vpnRef(g))
	case waited:
		return fmt.Sprintf("%s: changes applied.", vpnRef(g))
	case out.Coalesced:
		return fmt.Sprintf("%s is already being updated; these changes go with it%s.", vpnRef(g), op)
	case out.Mode == "rebuild":
		return fmt.Sprintf("Rebuilding %s: offline for a few minutes%s.", vpnRef(g), op)
	}
	return fmt.Sprintf("Applying the changes to %s%s.", vpnRef(g), op)
}

func vpnProfileCmd(a *App) *cobra.Command {
	var save, force bool
	cmd := &cobra.Command{
		Use:   "profile <gateway> [file]",
		Short: "The connection profile people import in the VPN app (to a file, readable by you only)",
		Long: "The OpenVPN profile of a gateway: each person imports it and signs in with their own\n" +
			"username and password. It carries the gateway's shared client certificate, so it is\n" +
			"written readable by you only (0600); without a file it is printed. --save writes it under\n" +
			"the name the API suggests. Needs a token with network:write.",
		Example: "  mw vpn profile office office.ovpn\n  mw vpn profile office --save\n  mw vpn profile office | pbcopy",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			g, err := a.resolveVPN(ctx, args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var p api.VPNProfile
			if err := c.Do(ctx, "GET", "/vpn-gateways/"+g.ID+"/profile", nil, nil, &p); err != nil {
				return notOffered(err, "VPN profiles from the API")
			}
			if a.JSON {
				return a.encode(p)
			}
			target := ""
			switch {
			case len(args) == 2:
				target = args[1]
			case save:
				target = filepath.Base(strings.TrimSpace(p.Filename))
				if target == "" || target == "." || target == string(filepath.Separator) {
					target = "vpn-" + g.ID[:8] + ".ovpn"
				}
			}
			if target == "" || target == "-" {
				content := p.Content
				if !strings.HasSuffix(content, "\n") {
					content += "\n"
				}
				_, err := io.WriteString(a.Out, content)
				return err
			}
			if err := writePrivateFile(target, p.Content, force); err != nil {
				return err
			}
			fmt.Fprintf(a.Err, "Wrote %s's VPN profile to %s (readable by you only).\n", vpnRef(*g), target)
			fmt.Fprintf(a.Err, "Import it in the OpenVPN app; each person signs in with their own login (`mw vpn users %s`).\n", vpnRef(*g))
			return nil
		},
	}
	cmd.Flags().BoolVar(&save, "save", false, "write it to the file name the API suggests, here")
	cmd.Flags().BoolVar(&force, "force", false, "replace the file if it exists")
	return cmd
}

// writePrivateFile writes content readable by its owner only.
func writePrivateFile(path, content string, force bool) error {
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s exists: pass --force to replace it", path)
	}
	if err != nil {
		return err
	}
	// A file replaced with --force keeps the mode it had: make it private too.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Users.

func vpnUsersCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "users <gateway>",
		Aliases: []string{"user", "logins"},
		Short:   "Who can connect to a gateway: add, change, remove (passwords are never shown again)",
		Example: "  mw vpn users office\n" +
			"  mw vpn users create office maria --email maria@loja.com.br --send-credentials --apply\n" +
			"  mw vpn users update office maria --status inactive --apply\n" +
			"  mw vpn users delete office maria --apply",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			g, err := a.resolveVPN(ctx, args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			all, err := api.All[api.VPNUser](ctx, c, "/vpn-gateways/"+g.ID+"/users", nil)
			if err != nil {
				return notOffered(err, "VPN users from the API")
			}
			l := listing{headers: []string{"ID", "USERNAME", "EMAIL", "STATUS", "CREATED"},
				empty: fmt.Sprintf("Nobody can connect to %s yet: `mw vpn users create %s <username> --email …`.", vpnRef(*g), vpnRef(*g))}
			for _, u := range all {
				l.add(u.ID, u.ID, u.Username, u.Email, u.Status, whenPtr(u.CreatedAt))
			}
			if all == nil {
				all = []api.VPNUser{}
			}
			return a.printList(all, l)
		},
	}

	var email, password string
	var stdin, inactive, send, apply bool
	create := &cobra.Command{
		Use:   "create <gateway> <username>",
		Short: "Let someone connect: a login with their own password (generated and shown once unless given)",
		Long: "Add a login to a gateway. Without --password (or --password-stdin) the API generates one,\n" +
			"shown here once. --send-credentials emails the person their username, password and how to\n" +
			"connect. They can connect once the change is applied (`mw vpn apply`, or --apply).",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			username := args[1]
			if !vpnUsernameRe.MatchString(username) {
				return fmt.Errorf("%s is not a username: letters and digits only, up to 64", username)
			}
			if !strings.Contains(email, "@") {
				return errors.New("--email is the person's address (it must be unique on the gateway)")
			}
			if stdin {
				pw, err := readLine(a, "--password-stdin")
				if err != nil {
					return err
				}
				password = pw
			}
			if password != "" {
				if err := checkVPNPassword(password); err != nil {
					return err
				}
			}
			g, err := a.resolveVPN(ctx, args[0])
			if err != nil {
				return err
			}
			body := map[string]interface{}{"username": username, "email": strings.TrimSpace(email)}
			if password != "" {
				body["password"] = password
			}
			if inactive {
				body["status"] = "inactive"
			}
			if send {
				body["send_credentials"] = true
			}
			c, _ := a.Client()
			var u api.VPNUser
			if err := c.Do(ctx, "POST", "/vpn-gateways/"+g.ID+"/users", nil, body, &u); err != nil {
				return notOffered(err, "VPN users from the API")
			}
			if a.JSON {
				if err := a.encode(u); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(a.Out, "%s can connect to %s with their own login (%s).\n", u.Username, vpnRef(*g), u.Status)
				if u.Password != nil && *u.Password != "" {
					fmt.Fprintf(a.Out, "Password for %s, shown here once (`mw vpn users update %s %s --password-stdin` sets a new one):\n\n  %s\n\n",
						u.Username, vpnRef(*g), u.Username, *u.Password)
				}
				if send {
					fmt.Fprintf(a.Out, "Their credentials and how to connect were emailed to %s.\n", u.Email)
				}
			}
			return a.afterUserChange(ctx, g, apply, fmt.Sprintf("%s can connect after `mw vpn apply %s`.", u.Username, vpnRef(*g)))
		},
	}
	cf := create.Flags()
	cf.StringVar(&email, "email", "", "the person's email (unique on the gateway; where credentials go)")
	cf.StringVar(&password, "password", "", "their password, 8 to 128 characters (default: one is generated and shown once)")
	cf.BoolVar(&stdin, "password-stdin", false, "read their password from standard input")
	cf.BoolVar(&inactive, "inactive", false, "create the login refused, to turn on later")
	cf.BoolVar(&send, "send-credentials", false, "email the person their username, password and how to connect")
	cf.BoolVar(&apply, "apply", false, "apply the change right away")

	get := &cobra.Command{
		Use:   "get <gateway> <user>",
		Short: "A login (by username, email or id)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, u, err := a.resolveVPNUser(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			return a.printDetail(u, vpnUserDetail(*u))
		},
	}

	var newName, newEmail, newPassword, status string
	var updStdin, updSend, updApply bool
	update := &cobra.Command{
		Use:   "update <gateway> <user>",
		Short: "Change a login: a new password, email or username, or refuse it (--status inactive)",
		Example: "  mw vpn users update office maria --status inactive --apply\n" +
			"  printf '%s\\n' \"$NEW\" | mw vpn users update office maria --password-stdin --send-credentials",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			body := map[string]interface{}{}
			fl := cmd.Flags()
			if fl.Changed("username") {
				if !vpnUsernameRe.MatchString(newName) {
					return fmt.Errorf("%s is not a username: letters and digits only, up to 64", newName)
				}
				body["username"] = newName
			}
			if fl.Changed("email") {
				if !strings.Contains(newEmail, "@") {
					return errors.New("--email is the person's address")
				}
				body["email"] = strings.TrimSpace(newEmail)
			}
			if updStdin {
				pw, err := readLine(a, "--password-stdin")
				if err != nil {
					return err
				}
				newPassword = pw
			}
			if newPassword != "" {
				if err := checkVPNPassword(newPassword); err != nil {
					return err
				}
				body["password"] = newPassword
			}
			if fl.Changed("status") {
				st := strings.ToLower(status)
				if st != "active" && st != "inactive" {
					return errors.New("--status is active or inactive")
				}
				body["status"] = st
			}
			if updSend {
				body["send_credentials"] = true
			}
			if len(body) == 0 || (len(body) == 1 && updSend) {
				return errors.New("nothing to change: pass --username, --email, --password(-stdin) or --status")
			}
			g, u, err := a.resolveVPNUser(ctx, args[0], args[1])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var out api.VPNUser
			if err := c.Do(ctx, "PATCH", "/vpn-gateways/"+g.ID+"/users/"+u.ID, nil, body, &out); err != nil {
				return notOffered(err, "VPN users from the API")
			}
			if err := a.printDetail(out, vpnUserDetail(out)); err != nil {
				return err
			}
			if updSend && !a.JSON {
				fmt.Fprintf(a.Out, "Their credentials were emailed to %s.\n", out.Email)
			}
			return a.afterUserChange(ctx, g, updApply, fmt.Sprintf("It takes effect after `mw vpn apply %s`.", vpnRef(*g)))
		},
	}
	uf := update.Flags()
	uf.StringVar(&newName, "username", "", "a new username (letters and digits)")
	uf.StringVar(&newEmail, "email", "", "a new email")
	uf.StringVar(&newPassword, "password", "", "a new password, 8 to 128 characters")
	uf.BoolVar(&updStdin, "password-stdin", false, "read the new password from standard input")
	uf.StringVar(&status, "status", "", "active, or inactive: the login is kept but refused")
	uf.BoolVar(&updSend, "send-credentials", false, "email the person their new credentials")
	uf.BoolVar(&updApply, "apply", false, "apply the change right away")
	completeWith(update, "status", []string{"active", "inactive"})

	var yes, delApply bool
	remove := &cobra.Command{
		Use:     "delete <gateway> <user>",
		Aliases: []string{"remove", "rm"},
		Short:   "Remove a login: the person can no longer connect once applied",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			g, u, err := a.resolveVPNUser(ctx, args[0], args[1])
			if err != nil {
				return err
			}
			if !yes {
				ok, err := a.confirm(fmt.Sprintf("Remove %s's VPN access to %s? [y/N] ", u.Username, vpnRef(*g)), "--yes")
				if err != nil || !ok {
					if err == nil {
						a.printf("Nothing removed.\n")
					}
					return err
				}
			}
			c, _ := a.Client()
			if err := c.Do(ctx, "DELETE", "/vpn-gateways/"+g.ID+"/users/"+u.ID, nil, nil, nil); err != nil {
				return notOffered(err, "VPN users from the API")
			}
			if a.JSON {
				if err := a.encode(map[string]interface{}{"id": u.ID, "deleted": true}); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(a.Out, "%s's login removed from %s.\n", u.Username, vpnRef(*g))
			}
			return a.afterUserChange(ctx, g, delApply, fmt.Sprintf("%s can still connect until `mw vpn apply %s`.", u.Username, vpnRef(*g)))
		},
	}
	remove.Flags().BoolVarP(&yes, "yes", "y", false, "remove without asking")
	remove.Flags().BoolVar(&delApply, "apply", false, "apply the change right away")

	cmd.AddCommand(create, get, update, remove)
	return cmd
}

// afterUserChange applies a user change (--apply), or says it waits for one.
func (a *App) afterUserChange(ctx context.Context, g *api.VPNGateway, apply bool, pending string) error {
	if !apply {
		a.printf("%s\n", pending)
		return nil
	}
	out, err := a.vpnApply(ctx, g, false)
	if err != nil {
		return fmt.Errorf("saved, but not applied: %w", err)
	}
	a.printf("%s\n", applyText(*g, *out, false))
	return nil
}

func checkVPNPassword(pw string) error {
	if n := utf8.RuneCountInString(pw); n < 8 || n > 128 {
		return fmt.Errorf("a VPN password has 8 to 128 characters (this one has %d)", n)
	}
	return nil
}

func vpnUserDetail(u api.VPNUser) detail {
	var d detail
	d.add("ID", u.ID)
	d.add("Username", u.Username)
	d.add("Email", u.Email)
	status := u.Status
	if u.Status == "inactive" {
		status = "inactive (the login is refused)"
	}
	d.add("Status", status)
	d.add("Created", whenPtr(u.CreatedAt))
	d.add("Changed", whenPtr(u.UpdatedAt))
	return d
}

// resolveVPNUser finds a gateway's login by id, username or email.
func (a *App) resolveVPNUser(ctx context.Context, gatewayRef, ref string) (*api.VPNGateway, *api.VPNUser, error) {
	g, err := a.resolveVPN(ctx, gatewayRef)
	if err != nil {
		return nil, nil, err
	}
	c, _ := a.Client()
	if uuidRe.MatchString(strings.ToLower(ref)) {
		var u api.VPNUser
		if err := c.Do(ctx, "GET", "/vpn-gateways/"+g.ID+"/users/"+strings.ToLower(ref), nil, nil, &u); err != nil {
			return nil, nil, notOffered(err, "VPN users from the API")
		}
		return g, &u, nil
	}
	all, err := api.All[api.VPNUser](ctx, c, "/vpn-gateways/"+g.ID+"/users", nil)
	if err != nil {
		return nil, nil, notOffered(err, "VPN users from the API")
	}
	for _, u := range all {
		if strings.EqualFold(u.Username, ref) || strings.EqualFold(u.Email, ref) {
			u := u
			return g, &u, nil
		}
	}
	return nil, nil, fmt.Errorf("no login %s on %s (`mw vpn users %s` lists them)", ref, vpnRef(*g), vpnRef(*g))
}

// Site-to-site (read only).

func vpnSitesCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sites <gateway>",
		Aliases: []string{"site", "site-connections"},
		Short:   "The sites connected to a gateway (offices, other clouds) and their tunnels' state",
		Long: "The site-to-site connections of a gateway — IPsec or WireGuard — and whether each tunnel is\n" +
			"up. Keys are never shown. They are created and changed in the console.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			g, err := a.resolveVPN(ctx, args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			all, err := api.All[api.VPNSiteConnection](ctx, c, "/vpn-gateways/"+g.ID+"/site-connections", nil)
			if err != nil {
				return notOffered(err, "VPN site connections from the API")
			}
			l := listing{headers: []string{"ID", "NAME", "PROTOCOL", "PEER", "NETWORKS", "TUNNEL", "STATUS", "LAST HANDSHAKE"},
				empty: fmt.Sprintf("No sites connected to %s: connect one in the console (VPN gateway › Site-to-site).", vpnRef(*g))}
			for _, s := range all {
				l.add(s.ID, s.ID, s.Name, siteProtocolText(s.Protocol), orDash(firstNonEmpty(str(s.PeerAddress), s.PeerVendor)),
					orDash(strings.Join(s.RemoteNetworks, ",")), tunnelText(s), siteStatusText(s), whenPtr(s.LastHandshakeAt))
			}
			if all == nil {
				all = []api.VPNSiteConnection{}
			}
			return a.printList(all, l)
		},
	}
	get := &cobra.Command{
		Use:   "get <gateway> <site>",
		Short: "A site connection: its tunnel, traffic and why it is down when it is",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			g, err := a.resolveVPN(ctx, args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var s api.VPNSiteConnection
			ref := args[1]
			if uuidRe.MatchString(strings.ToLower(ref)) {
				err = c.Do(ctx, "GET", "/vpn-gateways/"+g.ID+"/site-connections/"+strings.ToLower(ref), nil, nil, &s)
			} else {
				var all []api.VPNSiteConnection
				all, err = api.All[api.VPNSiteConnection](ctx, c, "/vpn-gateways/"+g.ID+"/site-connections", nil)
				found := false
				for _, x := range all {
					if strings.EqualFold(x.Name, ref) {
						s, found = x, true
						break
					}
				}
				if err == nil && !found {
					return fmt.Errorf("no site %s on %s (`mw vpn sites %s` lists them)", ref, vpnRef(*g), vpnRef(*g))
				}
			}
			if err != nil {
				return notOffered(err, "VPN site connections from the API")
			}
			return a.printDetail(s, siteDetail(*g, s))
		},
	}
	cmd.AddCommand(get)
	return cmd
}

func siteDetail(g api.VPNGateway, s api.VPNSiteConnection) detail {
	var d detail
	d.add("ID", s.ID)
	d.add("Name", s.Name)
	d.add("Gateway", vpnRef(g))
	d.add("Protocol", siteProtocolText(s.Protocol))
	d.add("Status", siteStatusText(s))
	d.add("Tunnel", tunnelText(s))
	if s.ErrorDetail != nil && *s.ErrorDetail != "" {
		d.add("Why", *s.ErrorDetail)
	}
	d.add("Peer", strings.TrimSpace(firstNonEmpty(str(s.PeerAddress), "dials in")+" "+parens(strings.ReplaceAll(s.PeerVendor, "_", " "))))
	d.add("Their networks", orDash(strings.Join(s.RemoteNetworks, ", ")))
	d.add("Last handshake", whenPtr(s.LastHandshakeAt))
	d.add("Last up", whenPtr(s.LastUpAt))
	d.add("Traffic", bytesText(s.BytesIn)+" in, "+bytesText(s.BytesOut)+" out")
	d.add("Applied", whenPtr(s.AppliedAt))
	d.add("Created", when(s.CreatedAt))
	return d
}

func siteProtocolText(p string) string {
	switch p {
	case "ipsec_ikev2":
		return "IPsec (IKEv2)"
	case "wireguard":
		return "WireGuard"
	}
	return p
}

func tunnelText(s api.VPNSiteConnection) string {
	t := s.TunnelState
	if s.ErrorClass != "" && s.ErrorClass != "none" {
		t += " " + parens(strings.ReplaceAll(s.ErrorClass, "_", " "))
	}
	return t
}

func siteStatusText(s api.VPNSiteConnection) string {
	st := strings.ReplaceAll(s.Status, "_", " ")
	if !s.Enabled && s.Status != "disabled" {
		st += ", disabled"
	}
	return st
}

// Gateways.

// resolveVPN finds a gateway by id, name, instance id or public address.
func (a *App) resolveVPN(ctx context.Context, ref string) (*api.VPNGateway, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	ref = strings.TrimSpace(ref)
	if uuidRe.MatchString(strings.ToLower(ref)) {
		var g api.VPNGateway
		if err := c.Do(ctx, "GET", "/vpn-gateways/"+strings.ToLower(ref), nil, nil, &g); err != nil {
			return nil, notOffered(err, "VPN gateways from the API")
		}
		return &g, nil
	}
	all, err := api.All[api.VPNGateway](ctx, c, "/vpn-gateways", nil)
	if err != nil {
		return nil, notOffered(err, "VPN gateways from the API")
	}
	var found []api.VPNGateway
	for _, g := range all {
		if strings.EqualFold(str(g.Name), ref) || g.InstanceID == ref || (ref != "" && str(g.PublicIP) == ref) {
			found = append(found, g)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no VPN gateway %s (`mw vpn` lists them)", ref)
	case 1:
		return &found[0], nil
	}
	var ids []string
	for _, g := range found {
		ids = append(ids, g.ID)
	}
	return nil, fmt.Errorf("more than one VPN gateway is %s (%s): use the id", ref, strings.Join(ids, ", "))
}

// vpnRef is how a gateway is named in a sentence and in a command to run.
func vpnRef(g api.VPNGateway) string {
	return firstNonEmpty(strings.TrimSpace(str(g.Name)), g.ID)
}

func gatewayState(g api.VPNGateway) string {
	switch {
	case !g.Running:
		return "stopped"
	case g.ApplyState == "queued" || g.ApplyState == "applying":
		return "applying"
	case g.ApplyState == "failed":
		return "apply failed"
	}
	return "running"
}

func pendingText(g api.VPNGateway) string {
	if !g.PendingChanges {
		return "—"
	}
	if g.ApplyMode == "rebuild" {
		return "pending (rebuild)"
	}
	return "pending"
}

func gatewayDetail(g api.VPNGateway) detail {
	var d detail
	d.add("ID", g.ID)
	d.add("Name", orDashPtr(g.Name))
	d.add("Instance", g.InstanceID)
	d.add("Location", orDashPtr(g.Location))
	d.add("Public IP", orDashPtr(g.PublicIP))
	d.add("State", gatewayState(g))
	d.add("Protocol", strings.ToUpper(g.Protocol))
	pool := orDashPtr(g.RemoteAccessPool)
	if issue := str(g.RemoteAccessPoolIssue); issue != "" {
		pool += " — " + issue
	}
	d.add("Address pool", pool)
	d.add("All traffic through it", yesNo(g.RouteAllTraffic))
	d.add("Shared logins", yesNo(g.AllowSharedLogins))
	d.add("MTU", strconv.Itoa(g.MTU))
	if g.WireGuard.PublicKey != nil && *g.WireGuard.PublicKey != "" {
		d.add("WireGuard", fmt.Sprintf("%s (port %d)", *g.WireGuard.PublicKey, g.WireGuard.ListenPort))
	}
	d.add("Users", strconv.Itoa(g.UsersCount))
	d.add("Site connections", fmt.Sprintf("%d of %d included", g.SiteConnectionsCount, g.IncludedSiteConnections))
	switch {
	case g.PendingChanges && g.ApplyMode == "rebuild":
		d.add("Pending changes", fmt.Sprintf("yes, they need a rebuild: `mw vpn apply %s --rebuild`", vpnRef(g)))
	case g.PendingChanges:
		d.add("Pending changes", fmt.Sprintf("yes: `mw vpn apply %s`", vpnRef(g)))
	default:
		d.add("Pending changes", "no")
	}
	if g.ApplyOperationID != nil {
		d.add("Applying", "operation "+*g.ApplyOperationID)
	}
	d.add("Last applied", whenPtr(g.AppliedAt))
	if g.LastApplyErrorCode != nil && *g.LastApplyErrorCode != "" {
		d.add("Last apply error", *g.LastApplyErrorCode)
	}
	if g.UpdateAvailable {
		d.add("Software", "an update is available (in the console)")
	}
	d.add("Created", whenPtr(g.CreatedAt))
	return d
}

// isNotFound says whether an error is the API not finding something (not a
// missing route).
func isNotFound(err error) bool {
	var p *api.Problem
	return errors.As(err, &p) && p.Status == http.StatusNotFound && !routeMissing(err)
}
