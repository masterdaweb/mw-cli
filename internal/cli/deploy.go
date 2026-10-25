package cli

import (
	"context"
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

// followDeployment prints a deployment's steps as they finish, until it ends.
func (a *App) followDeployment(ctx context.Context, d *api.Deployment) error {
	c, err := a.Client()
	if err != nil {
		return err
	}
	seen := map[string]string{}
	for {
		for _, s := range d.Steps {
			// A step is told once it starts, then when it ends.
			if s.Status == "PENDING" || seen[s.Kind] == s.Status {
				continue
			}
			seen[s.Kind] = s.Status
			msg := ""
			if s.Message != nil && *s.Message != "" {
				msg = " — " + *s.Message
			}
			a.printf("  %-13s %s%s\n", strings.ReplaceAll(strings.ToLower(s.Kind), "_", " "), strings.ToLower(s.Status), msg)
		}
		if finished[d.Status] {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollEvery):
		}
		var fresh api.Deployment
		if err := c.Do(ctx, "GET", "/serverless/deployments/"+d.ID, nil, nil, &fresh); err != nil {
			return err
		}
		*d = fresh
	}
	switch d.Status {
	case "LIVE":
		a.printf("Deployment #%d is live.\n", d.Number)
		return nil
	case "FAILED":
		if f := d.Failure; f != nil {
			msg := f.Message
			if f.Hint != nil && *f.Hint != "" {
				msg += "\n  " + *f.Hint
			}
			if f.LogTail != nil && *f.LogTail != "" {
				msg += "\n\n" + *f.LogTail
			}
			return fmt.Errorf("deployment #%d failed: %s", d.Number, msg)
		}
		return fmt.Errorf("deployment #%d failed", d.Number)
	default:
		return fmt.Errorf("deployment #%d ended %s", d.Number, strings.ToLower(d.Status))
	}
}

// startDeployment asks for one and, unless --detach, follows it.
func (a *App) startDeployment(cmd *cobra.Command, s *api.Service, body map[string]interface{}, detach bool) error {
	c, _ := a.Client()
	var d api.Deployment
	if err := c.Do(cmd.Context(), "POST", "/serverless/services/"+s.ID+"/deployments", nil, body, &d); err != nil {
		return err
	}
	if a.JSON || detach {
		return a.print(d, func(w *tabwriter.Writer) { fmt.Fprintf(w, "Deployment #%d of %s queued.\n", d.Number, s.Name) })
	}
	a.printf("Deployment #%d of %s:\n", d.Number, s.Name)
	return a.followDeployment(cmd.Context(), &d)
}

func deployCmd(a *App) *cobra.Command {
	var service, project, image, ref, message string
	var detach, noCache bool
	cmd := &cobra.Command{
		Use:   "deploy [folder]",
		Short: "Deploy a folder, the latest commit (or a ref), or a new image, and follow it",
		Long: "Deploy a service and print its steps as they finish, until it is live or has failed.\n\n" +
			"A service made with `mw init` deploys a folder: this one (or the one linked to it), or\n" +
			"the folder given. Its .mwignore — or else its .gitignore files — say what is left out.\n" +
			"A service built from Git deploys the tip of its branch, or --ref; one that runs an image\n" +
			"deploys a new one with --image.",
		Example: "  mw deploy\n  mw deploy ./site --service web\n  mw deploy --ref v1.4.2\n  mw deploy --image ghcr.io/acme/api:1.5",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			if s.Source.Kind == "UPLOAD" {
				if image != "" || ref != "" {
					return fmt.Errorf("%s is deployed from a folder: --image and --ref are for services built from a repository or an image", s.Name)
				}
				dir := a.Dir
				if len(args) == 1 {
					dir = args[0]
				} else if service == "" {
					// The folder that is linked, even from inside it.
					if _, at, _ := config.FindLink(a.Dir); at != "" {
						dir = at
					}
				}
				return a.deployFolder(cmd, s, dir, message, noCache, detach)
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
				return a.startDeployment(cmd, s, map[string]interface{}{"action": "latest"}, detach)
			}
			if ref != "" {
				return a.startDeployment(cmd, s, map[string]interface{}{"action": "ref", "ref": ref, "no_cache": noCache}, detach)
			}
			return a.startDeployment(cmd, s, map[string]interface{}{"action": "latest", "no_cache": noCache}, detach)
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().StringVar(&image, "image", "", "deploy this image (the service's source becomes it)")
	cmd.Flags().StringVar(&ref, "ref", "", "a commit, branch or tag to deploy (services built from Git)")
	cmd.Flags().StringVarP(&message, "message", "m", "", "what this deployment is, shown on it (a folder deploy)")
	cmd.Flags().BoolVar(&noCache, "no-cache", false, "build without the build cache")
	cmd.Flags().BoolVar(&detach, "detach", false, "do not wait for the deployment to finish")
	return cmd
}

// deployFolder packs a folder, uploads it and follows the deployment that
// builds it.
func (a *App) deployFolder(cmd *cobra.Command, s *api.Service, dir, message string, noCache, detach bool) error {
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
	if message == "" {
		message = "mw deploy from " + filepath.Base(abs)
		if sha := gitHead(abs); sha != "" {
			message += " at " + sha
		}
	}
	q := url.Values{"message": {message}}
	if noCache {
		q.Set("no_cache", "true")
	}
	var d api.Deployment
	if err := c.Upload(cmd.Context(), "/serverless/services/"+s.ID+"/uploads", q, res.Path, "application/gzip", &d); err != nil {
		return err
	}
	if a.JSON || detach {
		return a.print(d, func(w *tabwriter.Writer) { fmt.Fprintf(w, "Deployment #%d of %s queued.\n", d.Number, s.Name) })
	}
	a.printf("Deployment #%d of %s:\n", d.Number, s.Name)
	return a.followDeployment(cmd.Context(), &d)
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
	var noCache, detach bool
	cmd := &cobra.Command{
		Use:   "redeploy",
		Short: "Build and deploy the same source again (--no-cache: without the build cache)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			return a.startDeployment(cmd, s, map[string]interface{}{"action": "redeploy", "no_cache": noCache}, detach)
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().BoolVar(&noCache, "no-cache", false, "build without the build cache")
	cmd.Flags().BoolVar(&detach, "detach", false, "do not wait for the deployment to finish")
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
			return a.followDeployment(cmd.Context(), &d)
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
		Short: "A service's deployments (--revisions: what rollback goes back to; cancel: stop one)",
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
						src := ""
						if r.CommitSHA != nil {
							src = shortSHA(*r.CommitSHA)
						} else if r.Image != nil {
							src = *r.Image
						}
						fmt.Fprintf(w, "%d\t%s\t%v\t%s\t%s\n", r.Number, strings.ToLower(r.Status), r.Live, src, r.CreatedAt)
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
	cmd.AddCommand(deploymentsCancelCmd(a))
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
