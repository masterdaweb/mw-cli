package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// Following a deployment to its end: its steps as they start and finish, what
// the build prints while it builds, and — live — the address it answers at.
//
// The deployment runs on the platform, not here: a follow that loses the API
// for a while keeps trying, and Ctrl+C only stops the following.

// followPatience is how long a follow keeps trying while the API does not
// answer (a restart, a network that dropped); tests shorten it.
var followPatience = 5 * time.Minute

// follow is what a follow says beyond the steps.
type follow struct {
	// service is the one deployed, for its address once live; nil, not said.
	service *api.Service
	// buildLog prints what the build prints, while it runs.
	buildLog bool
	// firstSteps adds, once live, what to run next (a first deployment).
	firstSteps bool
}

// followDeployment follows d with the build's output, and no address.
func (a *App) followDeployment(ctx context.Context, d *api.Deployment) error {
	return a.followDeploymentWith(ctx, d, follow{buildLog: true})
}

// followDeploymentWith prints a deployment's steps as they start and finish,
// until it ends.
func (a *App) followDeploymentWith(ctx context.Context, d *api.Deployment, f follow) error {
	c, err := a.Client()
	if err != nil {
		return err
	}
	seen := map[string]string{}
	toldRollout := false
	// The build's output: read while the build step runs, and once more when
	// it ends, for its last lines.
	var build logTail
	streaming, streamed := false, false
	var lostSince time.Time
	for {
		building := stepStatus(d, "BUILD") == "RUNNING"
		if streaming {
			var view api.BuildLog
			if c.Do(ctx, "GET", "/serverless/deployments/"+d.ID+"/build-log", nil, nil, &view) == nil && view.State != "none" {
				for _, line := range build.next(view.Log) {
					fmt.Fprintf(a.Out, "  │ %s\n", line)
					streamed = true
				}
			}
		}
		streaming = building && f.buildLog && !a.JSON

		if rollingOut(d) && !toldRollout {
			toldRollout = true
			until := d.Rollout.Until
			if t, err := time.Parse(time.RFC3339, until); err == nil {
				until = t.Local().Format("15:04")
			}
			a.printf("  rolling out   %d%% of the requests go to the new version until %s; then all of them if it stays healthy\n"+
				"                (`mw deployments promote` takes them all now, `mw deployments abort` goes back)\n", d.Rollout.Percent, until)
		}
		for _, s := range d.Steps {
			// A step is told once it starts, then when it ends.
			if s.Status == "PENDING" || seen[s.Kind] == s.Status {
				continue
			}
			seen[s.Kind] = s.Status
			took := ""
			if s.DurationMS != nil && s.Status != "RUNNING" && *s.DurationMS >= 1000 {
				took = " in " + durationText(time.Duration(*s.DurationMS)*time.Millisecond)
			}
			msg := ""
			if s.Message != nil && *s.Message != "" {
				msg = " — " + *s.Message
			}
			a.printf("  %-13s %s%s%s\n", strings.ReplaceAll(strings.ToLower(s.Kind), "_", " "), strings.ToLower(s.Status), took, msg)
		}
		if finished[d.Status] {
			break
		}
		select {
		case <-ctx.Done():
			return stoppedFollowing(ctx, d)
		case <-time.After(pollEvery):
		}
		var fresh api.Deployment
		if err := c.Do(ctx, "GET", "/serverless/deployments/"+d.ID, nil, nil, &fresh); err != nil {
			if ctx.Err() != nil {
				return stoppedFollowing(ctx, d)
			}
			if !passing(err) {
				return err
			}
			// The API is away, the deployment is not: it goes on there.
			if lostSince.IsZero() {
				lostSince = time.Now()
				fmt.Fprintln(a.Err, "  (the API is not answering; the deployment goes on, still trying…)")
			} else if time.Since(lostSince) > followPatience {
				return fmt.Errorf("the API has not answered for %s: %w\n  Deployment #%d goes on: `mw deployments follow` picks it up again",
					durationText(followPatience), err, d.Number)
			}
			continue
		}
		if !lostSince.IsZero() {
			lostSince = time.Time{}
			fmt.Fprintln(a.Err, "  (the API answers again)")
		}
		*d = fresh
	}
	switch d.Status {
	case "LIVE":
		a.printf("%s\n", liveLine(d, f.service))
		if f.firstSteps {
			a.printf("Next: `mw logs -f` follows its logs, `mw env set KEY=value` gives it variables, `mw domains add` your own domain.\n")
		}
		return nil
	case "FAILED":
		if fail := d.Failure; fail != nil {
			msg := fail.Message
			if fail.Hint != nil && *fail.Hint != "" {
				msg += "\n  " + *fail.Hint
			}
			// The build's output was printed as it came: its last lines again say nothing new.
			inBuild := stepStatus(d, "BUILD") == "FAILED"
			if fail.LogTail != nil && *fail.LogTail != "" && !(streamed && inBuild) {
				msg += "\n\n" + *fail.LogTail
			}
			return fmt.Errorf("deployment #%d failed: %s", d.Number, msg)
		}
		return fmt.Errorf("deployment #%d failed", d.Number)
	default:
		return fmt.Errorf("deployment #%d ended %s", d.Number, strings.ToLower(d.Status))
	}
}

// stepStatus is the status of one of a deployment's steps, "" without it.
func stepStatus(d *api.Deployment, kind string) string {
	for _, s := range d.Steps {
		if s.Kind == kind {
			return s.Status
		}
	}
	return ""
}

// liveLine says a deployment is live: how long it took and, for a service
// that answers requests, where.
func liveLine(d *api.Deployment, s *api.Service) string {
	line := fmt.Sprintf("Deployment #%d is live", d.Number)
	if d.FinishedAt != nil {
		from, err1 := time.Parse(time.RFC3339, d.CreatedAt)
		to, err2 := time.Parse(time.RFC3339, *d.FinishedAt)
		if err1 == nil && err2 == nil && to.Sub(from) >= time.Second {
			line += " in " + durationText(to.Sub(from))
		}
	}
	switch {
	case s != nil && s.URL != nil && *s.URL != "":
		return line + ": " + *s.URL
	case s != nil && s.InternalURL != nil && *s.InternalURL != "":
		return line + ": " + *s.InternalURL + " (inside its project)"
	}
	return line + "."
}

// durationText: 18s, 2m 14s, 1h 02m.
func durationText(d time.Duration) string {
	secs := int(d.Round(time.Second) / time.Second)
	switch {
	case secs < 60:
		return strconv.Itoa(secs) + "s"
	case secs < 3600:
		if secs%60 == 0 {
			return strconv.Itoa(secs/60) + "m"
		}
		return fmt.Sprintf("%dm %02ds", secs/60, secs%60)
	}
	return fmt.Sprintf("%dh %02dm", secs/3600, secs%3600/60)
}

// passing: an error that says nothing of the deployment — the API away, the
// network down — as against a refusal, which will not change by asking again.
func passing(err error) bool {
	if errors.Is(err, api.ErrUnauthorized) {
		return false
	}
	var p *api.Problem
	if errors.As(err, &p) {
		return p.Status >= 500
	}
	return true
}

// stoppedFollowing ends a follow the user interrupted: the deployment is not
// touched, and the next commands are said. (A deadline is another matter: it
// is reported as the error it is.)
func stoppedFollowing(ctx context.Context, d *api.Deployment) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ctx.Err()
	}
	return &exitError{code: 130, msg: fmt.Sprintf(
		"\nStopped following: deployment #%d goes on. `mw deployments follow` picks it up again; `mw deployments cancel` stops it.", d.Number)}
}

// ── What the build printed, as it comes ────────────────────────────────────

// buildNoise is what BuildKit prints that says nothing to read as it goes by:
// that a step it had begun is still running while another's output is shown
// (`#6 ...`), and each tick of a layer being fetched or unpacked
// (`#2 sha256:5f75… 3.15MB / 8.85MB 0.5s`) — its `done` line is kept.
var buildNoise = regexp.MustCompile(`^#\d+ (\.\.\.|(extracting )?sha256:[0-9a-f]{64}( .*)?)$`)

// importNoise is each layer and each platform's manifest an image's import
// names as it copies them: dozens of digests for one image. Its first line
// (what is copied, to where) and its last (the image as it now is) say it.
var importNoise = regexp.MustCompile(`^\d{4}/\d\d/\d\d \d\d:\d\d:\d\d ((pushed|existing|mounted) blob: sha256:[0-9a-f]{64}|\S+@sha256:[0-9a-f]{64}: digest: sha256:[0-9a-f]{64} size: \d+)$`)

// noise: a line of the build's output not worth a line of the terminal.
func noise(line string) bool {
	return importNoise.MatchString(line) || (buildNoise.MatchString(line) && !strings.HasSuffix(line, " done"))
}

// lostTail is how many of a log's last lines are shown when nothing printed
// before is found in it any more (it moved on by more than what is kept).
const lostTail = 40

// logTail gives what is new in a log that is read again whole — or as its
// last lines, a window that moves — each time: the lines after the ones
// printed last.
type logTail struct {
	last []string
}

func (t *logTail) next(now string) []string {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(now, "\r\n", "\n"), "\n") {
		l = strings.TrimRight(l, " \t\r")
		if l == "" || noise(l) {
			continue
		}
		lines = append(lines, l)
	}
	start := 0
	if n := len(t.last); n > 0 && len(lines) > 0 {
		start = -1
		// The last lines printed, found again as late as they appear: three in a
		// row first, since one line alone (a step's title) repeats.
		for k := min(n, 3); k >= 1 && start < 0; k-- {
			want := t.last[n-k:]
			for i := len(lines) - 1; i >= k-1; i-- {
				if equalLines(lines[i-k+1:i+1], want) {
					start = i + 1
					break
				}
			}
		}
		if start < 0 {
			start = max(0, len(lines)-lostTail)
		}
	}
	fresh := lines[start:]
	t.last = append(t.last, fresh...)
	if len(t.last) > 8 {
		t.last = append([]string(nil), t.last[len(t.last)-8:]...)
	}
	return fresh
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ── `mw deployments follow` ────────────────────────────────────────────────

func deploymentsFollowCmd(a *App) *cobra.Command {
	var service, project string
	var noBuildLog bool
	cmd := &cobra.Command{
		Use:   "follow [<number>|<id>]",
		Short: "Follow a deployment to its end (default: the one under way, else the latest)",
		Long: "Print a deployment's steps as they start and finish, and what its build prints, until it\n" +
			"is live or has failed — what `mw deploy` does, for a deployment started elsewhere (a push,\n" +
			"the console) or one `mw deploy` stopped following. It exits non-zero when the deployment\n" +
			"did not go live, so a pipeline can wait on it.",
		Example: "  mw deployments follow\n  mw deployments follow 14 --service api",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var target *api.Deployment
			var s *api.Service
			if len(args) == 1 && uuidRe.MatchString(args[0]) {
				var d api.Deployment
				if err := c.Do(cmd.Context(), "GET", "/serverless/deployments/"+args[0], nil, nil, &d); err != nil {
					return err
				}
				target = &d
				var owner api.Service
				if c.Do(cmd.Context(), "GET", "/serverless/services/"+d.ServiceID, nil, nil, &owner) == nil {
					s = &owner
				}
			} else {
				if s, err = a.resolveService(cmd.Context(), service, project); err != nil {
					return err
				}
				var page api.Page[api.Deployment]
				if err := c.Do(cmd.Context(), "GET", "/serverless/services/"+s.ID+"/deployments", url.Values{"limit": {"50"}}, nil, &page); err != nil {
					return err
				}
				if len(page.Data) == 0 {
					return fmt.Errorf("%s has no deployment yet: `mw deploy` starts one", s.Name)
				}
				if len(args) == 1 {
					want := strings.TrimPrefix(args[0], "#")
					for i := range page.Data {
						if strconv.Itoa(page.Data[i].Number) == want {
							target = &page.Data[i]
						}
					}
					if target == nil {
						return fmt.Errorf("%s has no deployment #%s among its last 50", s.Name, want)
					}
				} else {
					// The one under way; with none, the latest says how it ended.
					target = &page.Data[0]
					for i := range page.Data {
						if !finished[page.Data[i].Status] {
							target = &page.Data[i]
							break
						}
					}
				}
			}
			name := ""
			if s != nil {
				name = " of " + s.Name
			}
			a.printf("Deployment #%d%s:\n", target.Number, name)
			err = a.followDeploymentWith(cmd.Context(), target, follow{service: s, buildLog: !noBuildLog})
			if a.JSON {
				// The deployment as it ended, for a script; the exit code still says how.
				if perr := a.print(target, nil); perr != nil {
					return perr
				}
			}
			return err
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().BoolVar(&noBuildLog, "no-build-log", false, "the steps alone, without what the build prints")
	return cmd
}
