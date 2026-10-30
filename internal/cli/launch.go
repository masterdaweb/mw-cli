package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

var frameworkNames = map[string]string{
	"laravel": "Laravel", "django": "Django", "rails": "Ruby on Rails", "nextjs": "Next.js", "node": "Node.js",
	"fastapi": "FastAPI", "flask": "Flask", "go": "Go", "php": "PHP", "dockerfile": "Dockerfile",
}

// providerHosts: where a pasted https address belongs to a provider that can
// be connected (and then deploys on every push).
var providerHosts = map[string]string{"github.com": "GITHUB", "gitlab.com": "GITLAB", "bitbucket.org": "BITBUCKET"}

func launchCmd(a *App) *cobra.Command {
	var name, branch, provider, connection, location string
	var without []string
	var yes, dryRun, detach bool
	cmd := &cobra.Command{
		Use:   "launch <repository>",
		Short: "Publish a repository as a whole project: its services, databases, Redis and bucket, wired",
		Long: "Reads a few files of the repository and proposes the project it needs: the site and the\n" +
			"command that migrates before each release, the queue worker and scheduler it has, the\n" +
			"databases, Redis and bucket it uses, its secrets generated, and what it costs. Confirm and\n" +
			"it is all made, bound and deployed.\n\n" +
			"The repository is owner/name of one connected in the console (Settings › Integrations:\n" +
			"it then deploys on every push), or the https address of a public one on GitHub, GitLab or\n" +
			"Bitbucket.",
		Example: "  mw launch ana/loja\n" +
			"  mw launch https://github.com/ana/loja --name loja\n" +
			"  mw launch ana/loja --without scheduler --without cache --yes\n" +
			"  mw launch ana/loja --dry-run\n" +
			"  mw launch ana/loja --location dallas",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			git, public, label, err := a.gitSource(cmd, args[0], provider, connection, branch)
			if err != nil {
				return err
			}
			var p api.SourceInspection
			err = c.Do(cmd.Context(), "POST", "/serverless/source-inspections", nil, map[string]interface{}{"git": git}, &p)
			// A connected account that cannot see or read the repository: it
			// may be someone else's public one.
			if public != nil && ((err != nil && errors.Is(err, api.Code("INVALID_SOURCE"))) || (err == nil && !p.Readable)) {
				git, public, p = public, nil, api.SourceInspection{}
				err = c.Do(cmd.Context(), "POST", "/serverless/source-inspections", nil, map[string]interface{}{"git": git}, &p)
			}
			if err != nil {
				return err
			}
			if !p.Readable {
				return fmt.Errorf("%s cannot be read from here: only repositories on GitHub, GitLab and Bitbucket are. Make its services one by one instead (`mw init` and `mw deploy`, or the console)", label)
			}
			keep, left, err := cut(&p, without)
			if err != nil {
				return err
			}
			if name == "" {
				name = nameFrom(repoName(label))
			}
			if dryRun {
				return a.print(p, func(w *tabwriter.Writer) { proposal(w, p, label, left) })
			}
			if !a.JSON {
				w := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
				proposal(w, p, label, left)
				if err := w.Flush(); err != nil {
					return err
				}
			}
			if !yes {
				if a.JSON {
					return errors.New("add --yes to publish without being asked (or --dry-run to see the proposal)")
				}
				ok, err := a.ask(fmt.Sprintf("\nPublish it as the project %q? [Y/n] ", name))
				if err != nil {
					return err
				}
				if !ok {
					a.printf("Nothing was made.\n")
					return nil
				}
			}
			var r api.TemplateDeployResult
			body := map[string]interface{}{"git": git, "project_name": name, "keep": keep}
			if location != "" {
				id, err := a.resolveLocation(cmd.Context(), appLocations, "projects", location)
				if err != nil {
					return err
				}
				body["datacenter_id"] = id
			}
			if err := c.Do(cmd.Context(), "POST", "/serverless/projects/from-source", nil, body, &r); err != nil {
				return err
			}
			if err := a.followProject(cmd, name, r, detach); err != nil {
				return err
			}
			if len(p.ToFill) > 0 && !a.JSON {
				a.printf("Still to fill in: %s (`mw env set KEY=value --service <service>`).\n", strings.Join(p.ToFill, ", "))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "the new project's name (default: the repository's)")
	cmd.Flags().StringVar(&location, "location", "", locationFlag)
	cmd.Flags().StringVar(&branch, "branch", "", "the branch to publish (default: the repository's default branch)")
	cmd.Flags().StringVar(&provider, "provider", "", "github, gitlab or bitbucket, when more than one is connected")
	cmd.Flags().StringVar(&connection, "connection", "", "the connected account's id, when one provider has several")
	cmd.Flags().StringArrayVar(&without, "without", nil, "leave a proposed service, database or bucket out, by its name (repeatable; the web service always stays)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "publish without asking")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the proposal and stop")
	cmd.Flags().BoolVar(&detach, "detach", false, "return once the project is made, without following the deployments")
	return cmd
}

// gitSource is the Git source the repository argument names. A connected
// account is preferred (it deploys on every push); `public` is the public-URL
// source to fall back to when that account cannot see the repository.
func (a *App) gitSource(cmd *cobra.Command, repo, provider, connection, branch string) (git, public map[string]interface{}, label string, err error) {
	withBranch := func(g map[string]interface{}) map[string]interface{} {
		if branch != "" {
			g["branch"] = branch
		}
		return g
	}
	fullName := repo
	if strings.Contains(repo, "://") {
		u, perr := url.Parse(repo)
		if perr != nil || u.Scheme != "https" || u.Host == "" || strings.Trim(u.Path, "/") == "" {
			return nil, nil, "", fmt.Errorf("%q is not a repository's https address, like https://github.com/ana/loja", repo)
		}
		fullName = strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
		label = strings.TrimPrefix(u.Host, "www.") + "/" + fullName
		public = withBranch(map[string]interface{}{"provider": "PUBLIC_URL", "repo_url": repo})
		host := providerHosts[strings.TrimPrefix(strings.ToLower(u.Host), "www.")]
		if host == "" || (provider != "" && !strings.EqualFold(provider, host)) {
			return public, nil, label, nil
		}
		provider = host
	} else {
		label = repo
		if strings.Count(repo, "/") < 1 || strings.HasPrefix(repo, "/") || strings.HasSuffix(repo, "/") {
			return nil, nil, "", fmt.Errorf("give the repository as owner/name (connected in the console) or as its https address, not %q", repo)
		}
	}
	c, err := a.Client()
	if err != nil {
		return nil, nil, "", err
	}
	var all []api.GitConnection
	if err := c.Do(cmd.Context(), "GET", "/serverless/git/connections", nil, nil, &all); err != nil {
		return nil, nil, "", err
	}
	var fit []api.GitConnection
	for _, k := range all {
		if k.Status != "ACTIVE" || (provider != "" && !strings.EqualFold(k.Provider, provider)) || (connection != "" && k.ID != connection) {
			continue
		}
		fit = append(fit, k)
	}
	switch {
	case len(fit) == 1:
		g := withBranch(map[string]interface{}{"provider": fit[0].Provider, "connection_id": fit[0].ID, "repo_full_name": fullName})
		return g, public, label, nil
	case public != nil:
		// A pasted address with no account connected for it: read it as public.
		return public, nil, label, nil
	case len(fit) == 0 && len(all) == 0:
		return nil, nil, "", errors.New("no Git account is connected: connect GitHub, GitLab or Bitbucket in the console (Settings › Integrations), or give a public repository's https address")
	case len(fit) == 0:
		return nil, nil, "", fmt.Errorf("no connected account fits: %s", accounts(all))
	default:
		return nil, nil, "", fmt.Errorf("more than one Git account is connected (%s): pick one with --provider or --connection", accounts(fit))
	}
}

func accounts(list []api.GitConnection) string {
	var out []string
	for _, k := range list {
		s := strings.ToLower(k.Provider) + " " + k.AccountLogin
		if k.Status != "ACTIVE" {
			s += " (" + strings.ToLower(strings.ReplaceAll(k.Status, "_", " ")) + ")"
		}
		out = append(out, s+" ["+k.ID+"]")
	}
	return strings.Join(out, ", ")
}

// repoName is the last part of owner/name or of an address.
func repoName(label string) string {
	return label[strings.LastIndex(label, "/")+1:]
}

// cut leaves out of the proposal what --without names, and says what of it to
// make. The web service always stays: it is the site.
func cut(p *api.SourceInspection, without []string) (keep map[string][]string, left []string, err error) {
	out := map[string]bool{}
	for _, w := range without {
		out[strings.TrimSpace(w)] = true
	}
	known := map[string]bool{}
	var services []api.InspectedService
	for _, s := range p.Services {
		known[s.Name] = true
		if !out[s.Name] {
			services = append(services, s)
			continue
		}
		if s.Kind == "WEB" {
			return nil, nil, fmt.Errorf("%s is the site: it always stays", s.Name)
		}
		left = append(left, s.Name)
	}
	var databases []api.TemplateDatabase
	for _, d := range p.Databases {
		known[d.Key] = true
		if out[d.Key] {
			left = append(left, d.Key)
		} else {
			databases = append(databases, d)
		}
	}
	var buckets []api.TemplateBucket
	for _, b := range p.Buckets {
		known[b.Key] = true
		if out[b.Key] {
			left = append(left, b.Key)
		} else {
			buckets = append(buckets, b)
		}
	}
	for w := range out {
		if !known[w] {
			names := make([]string, 0, len(known))
			for k := range known {
				names = append(names, k)
			}
			sort.Strings(names)
			return nil, nil, fmt.Errorf("nothing called %q is proposed (there is %s)", w, strings.Join(names, ", "))
		}
	}
	p.Services, p.Databases, p.Buckets = services, databases, buckets
	keep = map[string][]string{"services": {}, "databases": {}, "buckets": {}}
	for _, s := range services {
		keep["services"] = append(keep["services"], s.Name)
	}
	for _, d := range databases {
		keep["databases"] = append(keep["databases"], d.Key)
	}
	for _, b := range buckets {
		keep["buckets"] = append(keep["buckets"], b.Key)
	}
	return keep, left, nil
}

// proposal prints what the repository is and what publishing it makes.
func proposal(w *tabwriter.Writer, p api.SourceInspection, label string, left []string) {
	where := label
	if p.Branch != nil {
		where += " (branch " + *p.Branch + ")"
	}
	if p.Framework != nil {
		fw := frameworkNames[*p.Framework]
		if fw == "" {
			fw = *p.Framework
		}
		fmt.Fprintf(w, "%s app found in %s. Publishing it makes:\n\n", fw, where)
	} else {
		fmt.Fprintf(w, "No framework recognized in %s: it is published as one web service, built as the builder detects it.\n\n", where)
	}
	for _, s := range p.Services {
		what, run := serviceWhat(s)
		fmt.Fprintf(w, "  %s\t%s\t%s\n", s.Name, what, run)
	}
	for _, d := range p.Databases {
		fmt.Fprintf(w, "  %s\t%s\t%s\n", d.Key, engineNames[d.Engine], dataSizeText(d.Size))
	}
	for _, b := range p.Buckets {
		access := "Object Storage, private (signed links)"
		if b.Public {
			access = "Object Storage, public files"
		}
		fmt.Fprintf(w, "  %s\t%s\t\n", b.Key, access)
	}
	fmt.Fprintln(w)
	var secrets []string
	for _, v := range p.Variables {
		if v.Generated {
			secrets = append(secrets, v.Key)
		}
	}
	if len(secrets) > 0 {
		fmt.Fprintf(w, "Secrets generated:\t%s\n", strings.Join(secrets, ", "))
	}
	if len(p.ToFill) > 0 {
		fmt.Fprintf(w, "Left to fill in:\t%s\n", strings.Join(p.ToFill, ", "))
	}
	if len(left) > 0 {
		fmt.Fprintf(w, "Left out:\t%s\n", strings.Join(left, ", "))
	}
	if p.Estimate != nil {
		fmt.Fprintf(w, "Cost:\t%s\n", estimateText(*p.Estimate, len(p.Databases) > 0))
	}
}

// serviceWhat: what a proposed service is, and what it runs.
func serviceWhat(s api.InspectedService) (string, string) {
	run := ""
	if s.StartCommand != nil {
		run = *s.StartCommand
	}
	switch s.Kind {
	case "WEB":
		if s.ReleaseCommand != nil {
			if run != "" {
				run += "; "
			}
			run += "before each release: " + *s.ReleaseCommand
		}
		return "site", run
	case "WORKER":
		return "worker", run
	case "CRON":
		if s.CronSchedule != nil && *s.CronSchedule == "* * * * *" {
			return "every minute", run
		}
		if s.CronSchedule != nil {
			return "cron " + *s.CronSchedule, run
		}
		return "cron", run
	default:
		return "private service", run
	}
}

// dataSizeText: a database size code, "pg-1-1" → "1 vCPU, 1 GB RAM".
func dataSizeText(code string) string {
	parts := strings.Split(code, "-")
	if len(parts) != 3 {
		return code
	}
	return parts[1] + " vCPU, " + parts[2] + " GB RAM"
}

// ask reads a yes or no; Enter is yes. No answer at all (no terminal, nothing
// piped) is a refusal that says how to go on.
func (a *App) ask(question string) (bool, error) {
	fmt.Fprint(a.Out, question)
	line, err := bufio.NewReader(a.In).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(a.Out)
			return false, errors.New("no answer: add --yes to publish without being asked")
		}
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes", "s", "sim":
		return true, nil
	default:
		return false, nil
	}
}
