package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/masterdaweb/mw-cli/internal/archive"
	"github.com/masterdaweb/mw-cli/internal/config"
	"github.com/spf13/cobra"
)

// How often a deployment, a database or the logs are polled (tests shorten it).
var pollEvery = 3 * time.Second

// Terminal deployment states: nothing more happens to a deployment in one.
var finished = map[string]bool{"LIVE": true, "REPLACED": true, "FAILED": true, "CANCELLED": true, "SUPERSEDED": true}

// deployRun is how one `mw deploy` (or redeploy) goes about it.
type deployRun struct {
	detach     bool
	noCache    bool
	noBuildLog bool
	message    string
	// first: the service was made by this very command, so what to do next is said.
	first bool
}

// startDeployment asks for one and, unless --detach, follows it.
func (a *App) startDeployment(cmd *cobra.Command, s *api.Service, body map[string]interface{}, run deployRun) error {
	c, _ := a.Client()
	var d api.Deployment
	if err := c.Do(cmd.Context(), "POST", "/serverless/services/"+s.ID+"/deployments", nil, body, &d); err != nil {
		return err
	}
	return a.started(cmd, s, &d, run)
}

// started says a deployment began and, unless --detach (or -o json), follows it.
func (a *App) started(cmd *cobra.Command, s *api.Service, d *api.Deployment, run deployRun) error {
	if a.JSON || run.detach {
		return a.print(d, func(w *tabwriter.Writer) {
			fmt.Fprintf(w, "Deployment #%d of %s queued: `mw deployments follow --service %s` follows it.\n", d.Number, s.Name, s.Name)
		})
	}
	a.printf("Deployment #%d of %s:\n", d.Number, s.Name)
	return a.followDeploymentWith(cmd.Context(), d, follow{service: s, buildLog: !run.noBuildLog, firstSteps: run.first})
}

func deployCmd(a *App) *cobra.Command {
	var service, project, image, ref string
	var run deployRun
	var yes bool
	cmd := &cobra.Command{
		Use:   "deploy [folder]",
		Short: "Deploy a folder, the latest commit (or a ref), or a new image, and follow it",
		Long: "Deploy a service and follow it — its steps, what the build prints — until it is live, with\n" +
			"its address, or has failed, with why.\n\n" +
			"In a folder no service was made from yet, `mw deploy` offers to make one named after the\n" +
			"folder and deploys it: a first deployment is this one command (--yes answers for scripts;\n" +
			"`mw init` first, to choose the name, the project, the kind or the size).\n\n" +
			"A service made from a folder deploys that folder: this one (or the one linked to it), or\n" +
			"the folder given. Its .mwignore — or else its .gitignore files — say what is left out, and\n" +
			"its mw.toml, when it has one, sets the port, the commands, the size. A service built from\n" +
			"Git deploys the tip of its branch, or --ref; one that runs an image deploys a new one with\n" +
			"--image.\n\n" +
			"Ctrl+C stops the following, never the deployment: `mw deployments follow` picks it up again.",
		Example: "  mw deploy\n  mw deploy --yes\n  mw deploy ./site --service web\n  mw deploy --ref v1.4.2\n  mw deploy --image ghcr.io/acme/api:1.5",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Which folder: the one given, else this one — and the service it is linked to.
			dir := a.Dir
			if len(args) == 1 {
				if dir = args[0]; !filepath.IsAbs(dir) {
					dir = filepath.Join(a.Dir, dir)
				}
			}
			linked, linkedAt, err := config.FindLink(dir)
			if err != nil {
				return err
			}
			if linked == nil && len(args) == 1 {
				// A folder given from a linked one is deployed as that one's service.
				if linked, _, err = config.FindLink(a.Dir); err != nil {
					return err
				}
			}
			var s *api.Service
			switch {
			case service != "":
				s, err = a.resolveService(cmd.Context(), service, project)
			case linked != nil:
				s, err = a.resolveService(cmd.Context(), linked.ServiceID, project)
				if isNotFound(err) {
					return fmt.Errorf("the service this folder is linked to (%s) no longer exists: remove %s, then `mw deploy` makes a new one (or `mw link <service>` an existing one)",
						firstNonEmpty(linked.ServiceName, linked.ServiceID), filepath.Join(linkedAt, ".mw"))
				}
			case image != "" || ref != "":
				return errors.New("which service? pass --service, or run `mw link <service>` in this directory")
			default:
				// No service was made from this folder yet: a first deployment.
				if s, err = a.firstDeploy(cmd, dir, yes); err != nil || s == nil {
					return err
				}
				run.first = true
			}
			if err != nil {
				return err
			}
			if s.Source.Kind == "UPLOAD" {
				if image != "" || ref != "" {
					return fmt.Errorf("%s is deployed from a folder: --image and --ref are for services built from a repository or an image", s.Name)
				}
				if len(args) == 0 && service == "" && linkedAt != "" {
					// The folder that is linked, even from inside it.
					dir = linkedAt
				}
				return a.deployFolder(cmd, s, dir, run)
			}
			if len(args) == 1 {
				what := "its repository: push to it"
				if s.Source.Kind == "IMAGE" {
					what = "an image: pass --image"
				}
				return fmt.Errorf("%s deploys from %s, or run `mw deploy` without a folder", s.Name, what)
			}
			c, _ := a.Client()
			if image != "" {
				// A new image is a change of source; deployed at once.
				if err := c.Do(cmd.Context(), "PATCH", "/serverless/services/"+s.ID+"/source", nil,
					map[string]interface{}{"image": map[string]string{"ref": image}}, nil); err != nil {
					return err
				}
				return a.startDeployment(cmd, s, map[string]interface{}{"action": "latest"}, run)
			}
			if ref != "" {
				return a.startDeployment(cmd, s, map[string]interface{}{"action": "ref", "ref": ref, "no_cache": run.noCache}, run)
			}
			return a.startDeployment(cmd, s, map[string]interface{}{"action": "latest", "no_cache": run.noCache}, run)
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().StringVar(&image, "image", "", "deploy this image (the service's source becomes it)")
	cmd.Flags().StringVar(&ref, "ref", "", "a commit, branch or tag to deploy (services built from Git)")
	cmd.Flags().StringVarP(&run.message, "message", "m", "", "what this deployment is, shown on it (a folder deploy)")
	cmd.Flags().BoolVar(&run.noCache, "no-cache", false, "build without the build cache")
	cmd.Flags().BoolVar(&run.detach, "detach", false, "do not wait for the deployment to finish")
	cmd.Flags().BoolVar(&run.noBuildLog, "no-build-log", false, "follow the steps alone, without what the build prints")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "a folder with no service yet: make one named after it without asking")
	return cmd
}

// firstDeploy makes the service a folder with none is deployed as — named
// after the folder, in a project of the same name — once the user agrees (or
// --yes), and links the folder to it. A nil service with no error: declined.
func (a *App) firstDeploy(cmd *cobra.Command, dir string, yes bool) (*api.Service, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(abs); err != nil {
		return nil, err
	} else if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", dir)
	}
	name := nameFrom(filepath.Base(abs))
	found := detectApp(abs)
	if !yes {
		if a.JSON {
			return nil, errors.New("no service was made from this folder yet: add --yes to make one named after it and deploy it, or run `mw init` first (or pass --service)")
		}
		a.printf("No service was made from this folder yet.\n")
		a.printf("  %s\n", found.Text)
		if !found.Recognized {
			a.printf("  The build may not know what to do with it: a Dockerfile always works.\n")
		}
		ok, err := a.askOr(fmt.Sprintf("Make the service %q (web, in the project %q) and deploy this folder? [Y/n] ", name, name),
			"no service was made from this folder yet, and there is no terminal to ask on: `mw deploy --yes` makes one named after it and deploys it; `mw init` first chooses its name, project, kind and size; --service names an existing one")
		if err != nil {
			return nil, err
		}
		if !ok {
			a.printf("Nothing was made. `mw init` makes the service with the name, project, kind and size you choose; `mw link <service>` uses one that exists.\n")
			return nil, nil
		}
	} else {
		a.printf("%s\n", found.Text)
	}
	s, p, err := a.makeFolderService(cmd.Context(), abs, folderService{project: name, name: name, kind: "web", port: found.Port})
	if err != nil {
		return nil, err
	}
	a.printf("Service %s created in project %s, and this folder linked to it (.mw/link.json).\n", s.Name, p.Name)
	return s, nil
}

// deployFolder packs a folder, uploads it and follows the deployment that
// builds it.
func (a *App) deployFolder(cmd *cobra.Command, s *api.Service, dir string, run deployRun) error {
	c, _ := a.Client()
	var access api.Access
	if err := c.Do(cmd.Context(), "GET", "/serverless/access", nil, nil, &access); err != nil {
		return err
	}
	if access.UploadMaxBytes == nil {
		return errors.New("deploying from a folder is not available here yet")
	}
	abs, _ := filepath.Abs(dir)
	a.printf("Packing %s…\n", abs)
	res, err := archive.Pack(dir, *access.UploadMaxBytes)
	if err != nil {
		return err
	}
	defer os.Remove(res.Path)
	note := ""
	if res.Rules != "" {
		note = ", leaving out what " + res.Rules + " says"
	}
	a.printf("Uploading %s (%d files%s)…\n", archive.HumanBytes(res.Size), res.Files, note)
	message := run.message
	if message == "" {
		message = "mw deploy from " + filepath.Base(abs)
		if sha := gitHead(abs); sha != "" {
			message += " at " + sha
		}
	}
	q := url.Values{"message": {message}}
	if run.noCache {
		q.Set("no_cache", "true")
	}
	var d api.Deployment
	if err := c.Upload(cmd.Context(), "/serverless/services/"+s.ID+"/uploads", q, res.Path, "application/gzip", &d); err != nil {
		return err
	}
	return a.started(cmd, s, &d, run)
}

// gitHead is the folder's commit, marked when it has changes not committed;
// "" outside a repository or without git.
func gitHead(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	sha := strings.TrimSpace(string(out))
	if st, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output(); err == nil && len(strings.TrimSpace(string(st))) > 0 {
		sha += " (with changes not committed)"
	}
	return sha
}

func redeployCmd(a *App) *cobra.Command {
	var service, project string
	var run deployRun
	cmd := &cobra.Command{
		Use:   "redeploy",
		Short: "Build and deploy the same source again (--no-cache: without the build cache)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			return a.startDeployment(cmd, s, map[string]interface{}{"action": "redeploy", "no_cache": run.noCache}, run)
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().BoolVar(&run.noCache, "no-cache", false, "build without the build cache")
	cmd.Flags().BoolVar(&run.detach, "detach", false, "do not wait for the deployment to finish")
	cmd.Flags().BoolVar(&run.noBuildLog, "no-build-log", false, "follow the steps alone, without what the build prints")
	return cmd
}

func restartCmd(a *App) *cobra.Command {
	var service, project string
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart a service's instances (no build)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var d api.Deployment
			if err := c.Do(cmd.Context(), "POST", "/serverless/services/"+s.ID+"/restart", nil, nil, &d); err != nil {
				return err
			}
			return a.print(d, func(w *tabwriter.Writer) { fmt.Fprintf(w, "Restarting %s (deployment #%d).\n", s.Name, d.Number) })
		},
	}
	serviceFlag(cmd, &service, &project)
	return cmd
}

func rollbackCmd(a *App) *cobra.Command {
	var service, project string
	var detach bool
	cmd := &cobra.Command{
		Use:   "rollback <revision>",
		Short: "Go back to an earlier revision (its number, from `mw deployments --revisions`)",
		Long: "Go back to an earlier revision, at once and without a build. Databases do not go back\n" +
			"with it: restore one to before a deployment from the console.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var revs []api.Revision
			if err := c.Do(cmd.Context(), "GET", "/serverless/services/"+s.ID+"/revisions", nil, nil, &revs); err != nil {
				return err
			}
			want := strings.TrimPrefix(args[0], "#")
			var rev *api.Revision
			for i := range revs {
				if strconv.Itoa(revs[i].Number) == want || revs[i].ID == want {
					rev = &revs[i]
				}
			}
			if rev == nil {
				return fmt.Errorf("%s has no revision %s", s.Name, args[0])
			}
			if !rev.RollbackAvailable {
				return fmt.Errorf("revision %d can no longer be rolled back to (only the last 10 are kept)", rev.Number)
			}
			var d api.Deployment
			if err := c.Do(cmd.Context(), "POST", "/serverless/services/"+s.ID+"/rollback", nil, map[string]string{"revision_id": rev.ID}, &d); err != nil {
				return err
			}
			if a.JSON || detach {
				return a.print(d, func(w *tabwriter.Writer) { fmt.Fprintf(w, "Rolling back to revision %d.\n", rev.Number) })
			}
			a.printf("Rolling %s back to revision %d (deployment #%d):\n", s.Name, rev.Number, d.Number)
			return a.followDeploymentWith(cmd.Context(), &d, follow{service: s})
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().BoolVar(&detach, "detach", false, "do not wait for it to finish")
	return cmd
}

func deploymentsCmd(a *App) *cobra.Command {
	var service, project string
	var revisions bool
	var limit int
	cmd := &cobra.Command{
		Use:   "deployments",
		Short: "A service's deployments (follow: one to its end; --revisions: what rollback goes back to; cancel: stop one)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			c, _ := a.Client()
			if revisions {
				var revs []api.Revision
				if err := c.Do(cmd.Context(), "GET", "/serverless/services/"+s.ID+"/revisions", nil, nil, &revs); err != nil {
					return err
				}
				return a.print(revs, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "REVISION\tSTATUS\tLIVE\tSOURCE\tCREATED")
					for _, r := range revs {
						fmt.Fprintf(w, "%d\t%s\t%v\t%s\t%s\n", r.Number, strings.ToLower(r.Status), r.Live, revisionSource(r), r.CreatedAt)
					}
				})
			}
			var page api.Page[api.Deployment]
			if err := c.Do(cmd.Context(), "GET", "/serverless/services/"+s.ID+"/deployments", url.Values{"limit": {strconv.Itoa(limit)}}, nil, &page); err != nil {
				return err
			}
			return a.print(page.Data, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "#\tSTATUS\tTRIGGER\tCOMMIT\tCREATED")
				for _, d := range page.Data {
					commit := ""
					if d.CommitSHA != nil {
						commit = shortSHA(*d.CommitSHA)
						if d.CommitMessage != nil {
							commit += " " + firstLine(*d.CommitMessage)
						}
					} else if d.ImageRef != nil {
						commit = *d.ImageRef
					}
					fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", d.Number, strings.ToLower(d.Status), strings.ToLower(d.Trigger), commit, d.CreatedAt)
				}
			})
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().BoolVar(&revisions, "revisions", false, "list revisions (what rollback goes back to)")
	cmd.Flags().IntVar(&limit, "limit", 20, "how many")
	cmd.AddCommand(deploymentsFollowCmd(a))
	cmd.AddCommand(deploymentsCancelCmd(a))
	cmd.AddCommand(deploymentsRolloutCmd(a, "promote"))
	cmd.AddCommand(deploymentsRolloutCmd(a, "abort"))
	cmd.AddCommand(deploymentsBuildLogCmd(a))
	return cmd
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	if len(line) > 60 {
		return line[:57] + "..."
	}
	return line
}

func logsCmd(a *App) *cobra.Command {
	var service, project, since, search, status string
	var follow, access bool
	var limit int
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "A service's logs (-f to follow; --access: the requests it answered)",
		Long: "A service's logs, or with --access the HTTP requests a web or private service answered:\n" +
			"method, status, address, time taken and size (never the query string). -f keeps printing\n" +
			"new ones.",
		Example: "  mw logs -f\n  mw logs --access --status 5xx --since 6h\n  mw logs --access --search /checkout -f",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if status != "" && !access {
				return errors.New("--status is for the requests a service answered: add --access")
			}
			switch status {
			case "", "2xx", "3xx", "4xx", "5xx":
			default:
				return errors.New("--status is 2xx, 3xx, 4xx or 5xx")
			}
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			c, _ := a.Client()
			from := ""
			if since != "" {
				d, err := time.ParseDuration(since)
				if err != nil {
					return errors.New("--since is a duration, like 30m or 2h")
				}
				from = time.Now().Add(-d).UTC().Format(time.RFC3339)
			}
			if access {
				return a.accessLogs(cmd, s, from, search, status, limit, follow)
			}
			last := ""
			for {
				q := url.Values{"limit": {strconv.Itoa(limit)}}
				if from != "" {
					q.Set("since", from)
				}
				if search != "" {
					q.Set("search", search)
				}
				var logs api.Logs
				if err := c.Do(cmd.Context(), "GET", "/serverless/services/"+s.ID+"/logs", q, nil, &logs); err != nil {
					return err
				}
				for _, l := range logs.Lines {
					// A line at the instant asked from may come back again: skip what was printed.
					if last != "" && l.TS <= last {
						continue
					}
					if a.JSON {
						// One object per line (JSON Lines), so it can be streamed.
						raw, _ := json.Marshal(l)
						fmt.Fprintln(a.Out, string(raw))
					} else {
						fmt.Fprintf(a.Out, "%s  r%d %s  %s\n", l.TS, l.Revision, l.Instance, l.Message)
					}
					last = l.TS
				}
				if !follow {
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
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines")
	cmd.Flags().StringVar(&since, "since", "1h", "how far back to start, e.g. 30m, 6h")
	cmd.Flags().StringVar(&search, "search", "", "only lines containing this (with --access: in the path)")
	cmd.Flags().IntVar(&limit, "limit", 200, "lines per request")
	cmd.Flags().BoolVar(&access, "access", false, "the HTTP requests it answered instead of its logs")
	cmd.Flags().StringVar(&status, "status", "", "with --access: only 2xx, 3xx, 4xx or 5xx")
	return cmd
}

func scaleCmd(a *App) *cobra.Command {
	var service, project, size string
	var minI, maxI, concurrency, instances int
	cmd := &cobra.Command{
		Use:   "scale",
		Short: "Change a service's size and scaling (applied with a new revision)",
		Long: "Web and private services scale between --min and --max instances by --concurrency\n" +
			"(--min 0 sleeps when idle); workers run --instances. --size changes the instance size.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			settings := map[string]interface{}{}
			flags := cmd.Flags()
			if flags.Changed("size") {
				settings["size"] = size
			}
			if flags.Changed("min") {
				settings["min_instances"] = minI
			}
			if flags.Changed("max") {
				settings["max_instances"] = maxI
			}
			if flags.Changed("concurrency") {
				settings["concurrency"] = concurrency
			}
			if flags.Changed("instances") {
				settings["instances"] = instances
			}
			if len(settings) == 0 {
				return errors.New("nothing to change: pass --size, --min, --max, --concurrency or --instances")
			}
			c, _ := a.Client()
			var out struct {
				Service    api.Service     `json:"service"`
				Deployment *api.Deployment `json:"deployment"`
			}
			if err := c.Do(cmd.Context(), "PATCH", "/serverless/services/"+s.ID+"/settings", nil,
				map[string]interface{}{"settings": settings, "deploy": true}, &out); err != nil {
				return err
			}
			return a.print(out, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "%s: %s, %d to %d instances.\n", s.Name, sizeText(out.Service.Settings), out.Service.Settings.MinInstances, out.Service.Settings.MaxInstances)
			})
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().StringVar(&size, "size", "", "an instance size: x-small, small, medium or large (x-large on request)")
	cmd.Flags().IntVar(&minI, "min", 0, "minimum instances (0: sleeps when idle)")
	cmd.Flags().IntVar(&maxI, "max", 0, "maximum instances")
	cmd.Flags().IntVar(&concurrency, "concurrency", 0, "requests an instance takes at once")
	cmd.Flags().IntVar(&instances, "instances", 0, "instances of a worker")
	return cmd
}

// revisionSource is what a revision ran, in the customer's terms: its commit,
// else the digest of its image. Never the image's full reference — that names
// the platform's own registry (an older API still sends it), which is ours,
// not the customer's, and which nobody outside a cluster can pull from.
func revisionSource(r api.Revision) string {
	if r.CommitSHA != nil && *r.CommitSHA != "" {
		return shortSHA(*r.CommitSHA)
	}
	digest := ""
	if r.Digest != nil {
		digest = *r.Digest
	} else if r.Image != nil {
		if at := strings.LastIndex(*r.Image, "@"); at >= 0 {
			digest = (*r.Image)[at+1:]
		}
	}
	if strings.HasPrefix(digest, "sha256:") && len(digest) >= 19 {
		return digest[:19]
	}
	return digest
}
