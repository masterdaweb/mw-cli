// Package cli is the `mw` command line: everything the console does, from a
// terminal or a CI job, through the public API alone.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/masterdaweb/mw-cli/internal/config"
	"github.com/spf13/cobra"
)

// App is one run of the CLI: its streams, flags and the API client.
type App struct {
	Version string
	In      io.Reader
	Out     io.Writer
	Err     io.Writer
	Dir     string

	JSON   bool
	apiURL string
	token  string
	// dbDirect: database clients connect to the database's address (which
	// must allow this computer's), not through the platform's tunnel.
	dbDirect bool

	client *api.Client
}

// NewRoot builds the command tree.
func NewRoot(a *App) *cobra.Command {
	root := &cobra.Command{
		Use:           "mw",
		Short:         "Master da Web from the terminal: serverless apps, databases and storage",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       a.Version,
	}
	root.PersistentFlags().BoolVar(&a.JSON, "json", false, "print JSON (for scripts) instead of tables")
	root.PersistentFlags().StringVar(&a.apiURL, "api-url", "", "the API to talk to (default: the one you signed in to, or "+api.DefaultBaseURL+")")
	root.PersistentFlags().StringVar(&a.token, "token", "", "an API token for this command only (default: the signed-in one, or MW_TOKEN)")
	root.AddCommand(
		loginCmd(a), logoutCmd(a), whoamiCmd(a),
		projectsCmd(a), servicesCmd(a), initCmd(a), linkCmd(a), statusCmd(a),
		deployCmd(a), redeployCmd(a), restartCmd(a), rollbackCmd(a), deploymentsCmd(a), logsCmd(a), scaleCmd(a), runCmd(a),
		envCmd(a), domainsCmd(a), bindingsCmd(a),
		dbCmd(a, "db", ""), dbCmd(a, "redis", "valkey"),
		storageCmd(a), walletCmd(a), usageCmd(a), templatesCmd(a), launchCmd(a),
		groupsCmd(a), registryCmd(a),
		metricsCmd(a), alertsCmd(a), egressCmd(a), webhooksCmd(a),
	)
	return root
}

// Client is the API client for this run, from flags, the environment or the
// saved credentials.
func (a *App) Client() (*api.Client, error) {
	if a.client != nil {
		return a.client, nil
	}
	c, err := config.Load()
	if err != nil {
		return nil, err
	}
	base := firstNonEmpty(a.apiURL, c.APIURL, api.DefaultBaseURL)
	token := firstNonEmpty(a.token, c.Token)
	if token == "" {
		return nil, errors.New("not signed in: run `mw login` (or set MW_TOKEN)")
	}
	a.client = api.New(base, token, a.Version)
	a.client.OnWait = func(d time.Duration) {
		if d >= 2*time.Second {
			fmt.Fprintf(a.Err, "The API asked to slow down: trying again in %s…\n", d.Round(time.Second))
		}
	}
	return a.client, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// print writes v as JSON with --json, else the table.
func (a *App) print(v interface{}, table func(w *tabwriter.Writer)) error {
	if a.JSON {
		enc := json.NewEncoder(a.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	w := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
	table(w)
	return w.Flush()
}

func (a *App) printf(format string, args ...interface{}) {
	if !a.JSON {
		fmt.Fprintf(a.Out, format, args...)
	}
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// resolveService finds a service by id, by name (within --project when names
// repeat across projects), or from the directory's link.
func (a *App) resolveService(ctx context.Context, ref, project string) (*api.Service, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	if ref == "" {
		l, _, err := config.FindLink(a.Dir)
		if err != nil {
			return nil, err
		}
		if l == nil {
			return nil, errors.New("which service? pass --service, or run `mw link <service>` in this directory")
		}
		ref = l.ServiceID
	}
	if uuidRe.MatchString(ref) {
		var s api.Service
		if err := c.Do(ctx, "GET", "/serverless/services/"+ref, nil, nil, &s); err != nil {
			return nil, err
		}
		return &s, nil
	}
	q := url.Values{}
	if project != "" {
		p, err := a.resolveProject(ctx, project)
		if err != nil {
			return nil, err
		}
		q.Set("project_id", p.ID)
	}
	all, err := api.All[api.Service](ctx, c, "/serverless/services", q)
	if err != nil {
		return nil, err
	}
	var found []api.Service
	for _, s := range all {
		if s.Name == ref {
			found = append(found, s)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no service named %s", ref)
	case 1:
		return &found[0], nil
	default:
		var where []string
		for _, s := range found {
			where = append(where, s.ProjectName)
		}
		sort.Strings(where)
		return nil, fmt.Errorf("%s is a service in more than one project (%s): pass --project", ref, strings.Join(where, ", "))
	}
}

func (a *App) resolveProject(ctx context.Context, ref string) (*api.Project, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	if uuidRe.MatchString(ref) {
		var p api.Project
		if err := c.Do(ctx, "GET", "/serverless/projects/"+ref, nil, nil, &p); err != nil {
			return nil, err
		}
		return &p, nil
	}
	all, err := api.All[api.Project](ctx, c, "/serverless/projects", nil)
	if err != nil {
		return nil, err
	}
	for _, p := range all {
		if p.Name == ref {
			p := p
			return &p, nil
		}
	}
	return nil, fmt.Errorf("no project named %s", ref)
}

// Describe turns an error into what the user reads: the API's sentence, and
// what to do about the refusals that have an obvious next step.
func Describe(err error) string {
	var p *api.Problem
	if !errors.As(err, &p) {
		return err.Error()
	}
	msg := p.Error()
	switch {
	case p.Code == "insufficient_scope":
		msg += "\n  The token cannot do this. Create one with that scope under Settings → API tokens, then `mw login`."
	case strings.HasSuffix(p.Reason, "_CREDIT_REQUIRED") || p.Code == "insufficient_balance":
		msg += "\n  Add credit to your wallet in the console (Credits & Plans); `mw wallet` shows what is there."
	case p.Reason == "SERVERLESS_NOT_ENABLED":
		msg += "\n  Serverless is in beta: ask for access in the console."
	case p.Reason == "SERVERLESS_QUOTA_EXCEEDED":
		msg += quotaHint(p.Params)
	}
	if p.RequestID != "" {
		msg += fmt.Sprintf("\n  (request %s)", p.RequestID)
	}
	return msg
}

// quotaHint says, for each account limit a change passes, what is free and
// what the change needs — so one request to raise them asks for all.
func quotaHint(params map[string]interface{}) string {
	over, _ := params["over"].([]interface{})
	if len(over) == 0 {
		over = []interface{}{params}
	}
	var lines []string
	for _, o := range over {
		m, _ := o.(map[string]interface{})
		limit, _ := m["limit"].(float64)
		used, _ := m["used"].(float64)
		requested, _ := m["requested"].(float64)
		switch m["resource"] {
		case "vcpu_millis":
			lines = append(lines, fmt.Sprintf("vCPU: %s free of %s; this needs %s", milli(limit-used), milli(limit), milli(requested)))
		case "memory_mib":
			lines = append(lines, fmt.Sprintf("memory: %s GiB free of %s; this needs %s", gib(limit-used), gib(limit), gib(requested)))
		case "projects", "services":
			lines = append(lines, fmt.Sprintf("%s: %.0f of %.0f in use", m["resource"], used, limit))
		}
	}
	out := ""
	for _, l := range lines {
		out += "\n  " + l
	}
	return out + "\n  Ask for more in the console: Projects › Account limits › Ask for more."
}

func milli(v float64) string { return strconv.FormatFloat(v/1000, 'f', -1, 64) }
func gib(v float64) string   { return strconv.FormatFloat(v/1024, 'f', -1, 64) }

// Main runs the CLI and returns the process exit code.
func Main(version string, args []string) int {
	dir, _ := os.Getwd()
	a := &App{Version: version, In: os.Stdin, Out: os.Stdout, Err: os.Stderr, Dir: dir}
	root := NewRoot(a)
	root.SetArgs(args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		var exit *exitError
		if errors.As(err, &exit) {
			// A client (psql, redis-cli) said what went wrong already.
			if exit.msg != "" {
				fmt.Fprintln(os.Stderr, strings.TrimSpace(exit.msg))
			}
			return exit.code
		}
		fmt.Fprintln(os.Stderr, "error:", Describe(err))
		return 1
	}
	return 0
}
