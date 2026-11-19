package cli

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/masterdaweb/mw-cli/internal/config"
	"github.com/spf13/cobra"
)

var notLabel = regexp.MustCompile(`[^a-z0-9-]+`)

// nameFrom makes a folder's name a valid service or project name: lowercase
// letters, digits and hyphens, starting with a letter.
func nameFrom(folder string) string {
	n := notLabel.ReplaceAllString(strings.ToLower(folder), "-")
	n = strings.Trim(n, "-")
	if len(n) > 40 {
		n = strings.Trim(n[:40], "-")
	}
	if n == "" || n[0] < 'a' || n[0] > 'z' {
		n = "app-" + n
	}
	return strings.Trim(n, "-")
}

// folderService is the service `mw init` (or a first `mw deploy`) makes from a
// folder: its names, its kind and what it starts with.
type folderService struct {
	project, location string
	name, kind        string
	port              int
	size              string
	minInstances      *int
	maxInstances      *int
	instances         *int
	schedule          string
	timezone          string
}

// settings is what the service is created with, checked before anything is
// made: a cron job without a schedule, or scaling given to the wrong kind, is
// refused before a project exists.
func (f folderService) settings() (map[string]interface{}, error) {
	settings, err := cronSettings(f.kind, f.schedule, f.timezone)
	if err != nil {
		return nil, err
	}
	scales := strings.EqualFold(f.kind, "web") || strings.EqualFold(f.kind, "private")
	if (f.minInstances != nil || f.maxInstances != nil) && !scales {
		return nil, errors.New("--min-instances and --max-instances are for web and private services (a worker takes --instances)")
	}
	if f.instances != nil && !strings.EqualFold(f.kind, "worker") {
		return nil, errors.New("--instances is for --kind worker (web and private services scale between --min-instances and --max-instances)")
	}
	set := func(key string, value interface{}) {
		if settings == nil {
			settings = map[string]interface{}{}
		}
		settings[key] = value
	}
	if f.port > 0 {
		set("port", f.port)
	}
	if f.size != "" {
		set("size", f.size)
	}
	if f.minInstances != nil {
		set("min_instances", *f.minInstances)
	}
	if f.maxInstances != nil {
		set("max_instances", *f.maxInstances)
	}
	if f.instances != nil {
		set("instances", *f.instances)
	}
	return settings, nil
}

// makeFolderService makes the service a folder is deployed as — its project
// too, when there is none of that name — and links the folder to it. What
// would refuse the service is asked before the project is made, and a project
// this made for a service that was then refused is taken away again: a
// refusal leaves nothing behind.
func (a *App) makeFolderService(ctx context.Context, abs string, f folderService) (*api.Service, *api.Project, error) {
	settings, err := f.settings()
	if err != nil {
		return nil, nil, err
	}
	c, err := a.Client()
	if err != nil {
		return nil, nil, err
	}
	if err := a.creditForNewService(ctx); err != nil {
		return nil, nil, err
	}
	p, made, err := a.findOrCreateProject(ctx, f.project, f.location)
	if err != nil {
		return nil, nil, err
	}
	body := map[string]interface{}{
		"name":   f.name,
		"kind":   strings.ToUpper(f.kind),
		"source": map[string]interface{}{"kind": "UPLOAD"},
	}
	if settings != nil {
		body["settings"] = settings
	}
	var out struct {
		Service api.Service `json:"service"`
	}
	if err := c.Do(ctx, "POST", "/serverless/projects/"+p.ID+"/services", nil, body, &out); err != nil {
		if made {
			a.dropEmptyProject(ctx, p)
		}
		return nil, nil, err
	}
	s := out.Service
	if err := config.SaveLink(abs, config.Link{ServiceID: s.ID, ServiceName: s.Name, ProjectID: p.ID}); err != nil {
		return nil, nil, err
	}
	return &s, p, nil
}

// creditForNewService refuses, before anything is made, a new service the
// wallet cannot take: with what is there, what is needed and how to add it.
// A wallet that cannot be read (a token without the scope) decides nothing —
// the API still does.
func (a *App) creditForNewService(ctx context.Context) error {
	c, err := a.Client()
	if err != nil {
		return err
	}
	var w api.Wallet
	if err := c.Do(ctx, "GET", "/serverless/wallet", nil, nil, &w); err != nil {
		return nil
	}
	have, ok1 := new(big.Int).SetString(w.UsableMicroCents, 10)
	need, ok2 := new(big.Int).SetString(w.MinimumForNewService, 10)
	if !ok1 || !ok2 || have.Cmp(need) >= 0 {
		return nil
	}
	return errors.New(creditNeeded(w.MinimumForNewService, w.UsableMicroCents))
}

// creditNeeded says a new service needs more credit than the wallet holds,
// and the top-up that covers it (whole reais, rounded up).
func creditNeeded(needMicro, haveMicro string) string {
	msg := fmt.Sprintf("a new service needs %s of credit in the wallet, and it has %s", money(needMicro, 2), money(haveMicro, 2))
	need, ok1 := new(big.Int).SetString(needMicro, 10)
	have, ok2 := new(big.Int).SetString(haveMicro, 10)
	topup := "<reais>"
	if ok1 && ok2 && need.Cmp(have) > 0 {
		missing := new(big.Int).Sub(need, have)
		reais := new(big.Int).Add(missing, big.NewInt(999_999))
		topup = reais.Div(reais, big.NewInt(1_000_000)).String()
	}
	return msg + ".\n  `mw wallet topup " + topup + "` raises the invoice that adds it (or Credits & Plans in the console); nothing was made."
}

// dropEmptyProject takes away a project made a moment ago for a service that
// was then refused. Best effort: a project that stays is said, never an error
// over the refusal that matters.
func (a *App) dropEmptyProject(ctx context.Context, p *api.Project) {
	c, err := a.Client()
	if err != nil {
		return
	}
	// A new project is protected from deletion; this one holds nothing.
	err = c.Do(ctx, "PATCH", "/serverless/projects/"+p.ID, nil, map[string]interface{}{"deletion_protection": false}, nil)
	if err == nil {
		err = c.Do(ctx, "DELETE", "/serverless/projects/"+p.ID, url.Values{"confirm": {p.Name}}, nil, nil)
	}
	if err != nil {
		fmt.Fprintf(a.Err, "The project %s was made and stays, empty: `mw projects delete %s --confirm %s` removes it.\n", p.Name, p.Name, p.Name)
		return
	}
	a.printf("The project %s, made for it, was removed again.\n", p.Name)
}

func initCmd(a *App) *cobra.Command {
	var f folderService
	var minInstances, maxInstances, instances int
	var force bool
	cmd := &cobra.Command{
		Use:   "init [folder]",
		Short: "Make a service deployed from this folder, and link the folder to it",
		Long: "Make a service whose code is this folder (or the one given): `mw deploy` then packs it,\n" +
			"sends it and deploys it. The project is made too if it does not exist. Names default to\n" +
			"the folder's. The folder is linked (.mw/link.json), so commands run in it need no --service.\n\n" +
			"`mw deploy` alone, in a folder with no service yet, does both with the folder's name:\n" +
			"`mw init` is for choosing the name, the project, the kind, the size or where it runs.",
		Example: "  mw init\n  mw init --project loja --name api --port 8080\n  mw init --size x-small --max-instances 1\n  mw init --kind worker\n  mw init --name limpeza --kind cron --schedule '0 3 * * *'",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cmd.Flags()
			if flags.Changed("min-instances") {
				f.minInstances = &minInstances
			}
			if flags.Changed("max-instances") {
				f.maxInstances = &maxInstances
			}
			if flags.Changed("instances") {
				f.instances = &instances
			}
			// A cron job without a schedule is refused before any project is made.
			if _, err := f.settings(); err != nil {
				return err
			}
			dir := a.Dir
			if len(args) == 1 {
				dir = args[0]
			}
			abs, err := filepath.Abs(dir)
			if err != nil {
				return err
			}
			if l, at, err := config.FindLink(abs); err != nil {
				return err
			} else if l != nil && at == abs && !force {
				return fmt.Errorf("this folder is already linked to %s: `mw deploy` deploys it (or --force to make another service)", l.ServiceName)
			}
			base := nameFrom(filepath.Base(abs))
			if f.name == "" {
				f.name = base
			}
			if f.project == "" {
				f.project = base
			}
			// The port its Dockerfile exposes, unless one was given: a web or
			// private service listens there.
			found := detectApp(abs)
			if f.port == 0 && (strings.EqualFold(f.kind, "web") || strings.EqualFold(f.kind, "private")) {
				f.port = found.Port
			}
			s, p, err := a.makeFolderService(cmd.Context(), abs, f)
			if err != nil {
				return err
			}
			if a.JSON {
				return a.print(s, nil)
			}
			a.printf("Service %s created in project %s, and this folder linked to it.\n", s.Name, p.Name)
			a.printf("  %s\n", found.Text)
			a.printf("Next: `mw deploy` sends this folder and deploys it.\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&f.project, "project", "", "the project (default: the folder's name; made if missing)")
	cmd.Flags().StringVar(&f.location, "location", "", "where a project made now runs, for good: a location's id, name or code (`mw regions`)")
	cmd.Flags().StringVar(&f.name, "name", "", "the service's name (default: the folder's name)")
	cmd.Flags().StringVar(&f.kind, "kind", "web", "web, private, worker or cron")
	cmd.Flags().StringVar(&f.schedule, "schedule", "", "cron jobs: when it runs, a cron expression like '*/15 * * * *'")
	cmd.Flags().StringVar(&f.timezone, "timezone", "", "cron jobs: the schedule's time zone, e.g. America/Sao_Paulo (the platform's default when not given)")
	cmd.Flags().IntVar(&f.port, "port", 0, "the port the app listens on, as $PORT tells it (default 8080)")
	cmd.Flags().StringVar(&f.size, "size", "", "an instance size: x-small, small, medium or large (default: small)")
	cmd.Flags().IntVar(&minInstances, "min-instances", 0, "web and private: instances kept on without traffic (default 0: it sleeps)")
	cmd.Flags().IntVar(&maxInstances, "max-instances", 0, "web and private: the most instances it scales to (what the account's limits reserve)")
	cmd.Flags().IntVar(&instances, "instances", 0, "workers: how many run")
	cmd.Flags().BoolVar(&force, "force", false, "make a new service even if this folder is linked to one")
	return cmd
}

// findOrCreateProject is the project by name, made when there is none — in
// the location named, or the platform's default when none is. `made` says it
// was made now.
func (a *App) findOrCreateProject(ctx context.Context, name, location string) (p *api.Project, made bool, err error) {
	c, err := a.Client()
	if err != nil {
		return nil, false, err
	}
	all, err := api.All[api.Project](ctx, c, "/serverless/projects", nil)
	if err != nil {
		return nil, false, err
	}
	for _, p := range all {
		if p.Name == name || p.ID == name {
			p := p
			return &p, false, nil
		}
	}
	body := map[string]interface{}{"name": name}
	if location != "" {
		id, err := a.resolveLocation(ctx, appLocations, "projects", location)
		if err != nil {
			return nil, false, err
		}
		body["datacenter_id"] = id
	}
	var created api.Project
	if err := c.Do(ctx, "POST", "/serverless/projects", nil, body, &created); err != nil {
		return nil, false, err
	}
	a.printf("Project %s created in %s.\n", created.Name, locationText(a.locationNames(ctx, created.DatacenterID), created.DatacenterID))
	return &created, true, nil
}
