package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

var digitsRe = regexp.MustCompile(`^[0-9]+$`)

// resolveInstance finds an instance by its id or its name (or hostname).
func (a *App) resolveInstance(ctx context.Context, ref string) (*api.Instance, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("which instance? pass its id or name (`mw instances` lists them)")
	}
	if digitsRe.MatchString(ref) {
		var in api.Instance
		if err := c.Do(ctx, "GET", "/instances/"+ref, nil, nil, &in); err != nil {
			return nil, err
		}
		return &in, nil
	}
	all, err := api.All[api.Instance](ctx, c, "/instances", nil)
	if err != nil {
		return nil, err
	}
	var found []api.Instance
	for _, in := range all {
		if strings.EqualFold(str(in.Name), ref) || strings.EqualFold(str(in.Hostname), ref) {
			found = append(found, in)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no instance named %s", ref)
	case 1:
		return &found[0], nil
	default:
		var ids []string
		for _, in := range found {
			ids = append(ids, in.ID)
		}
		return nil, fmt.Errorf("more than one instance is named %s (%s): use the id", ref, strings.Join(ids, ", "))
	}
}

// resolveRegion takes a region id, name ("São Paulo") or code ("br-sp").
func (a *App) resolveRegion(ctx context.Context, ref string) (*api.Region, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	all, err := api.All[api.Region](ctx, c, "/regions", nil)
	if err != nil {
		return nil, err
	}
	if ref == "" {
		var open []api.Region
		for _, r := range all {
			if r.Available {
				open = append(open, r)
			}
		}
		if len(open) == 1 {
			return &open[0], nil
		}
		return nil, errors.New("which region? pass --region (`mw regions` lists them)")
	}
	for _, r := range all {
		if r.ID == ref || strings.EqualFold(r.Name, ref) || strings.EqualFold(str(r.RegionName), ref) {
			r := r
			return &r, nil
		}
	}
	return nil, fmt.Errorf("no region %s (`mw regions` lists them)", ref)
}

// instanceLabel is how an instance is named in a sentence.
func instanceLabel(in *api.Instance) string {
	if n := strings.TrimSpace(str(in.Name)); n != "" {
		return fmt.Sprintf("%s (%s)", n, in.ID)
	}
	return in.ID
}

func primaryIPv4(in api.Instance) string {
	for _, ip := range in.IPAddresses {
		if ip.Version == "ipv4" && ip.Primary {
			return ip.Address
		}
	}
	for _, ip := range in.IPAddresses {
		if ip.Version == "ipv4" {
			return ip.Address
		}
	}
	return ""
}

func num(p *float64) string {
	if p == nil {
		return "—"
	}
	return strconv.FormatFloat(*p, 'f', -1, 64)
}

// specsText is "2 vCPU · 4 GB · 80 GB".
func specsText(s api.InstanceSpecs) string {
	var parts []string
	if s.VCPUs != nil {
		parts = append(parts, num(s.VCPUs)+" vCPU")
	}
	if s.RAMMB != nil {
		parts = append(parts, ramText(*s.RAMMB))
	}
	if s.DiskGB != nil {
		parts = append(parts, num(s.DiskGB)+" GB disk")
	}
	return orDash(strings.Join(parts, " · "))
}

func ramText(mb float64) string {
	if mb >= 1024 && int64(mb)%1024 == 0 {
		return strconv.FormatInt(int64(mb)/1024, 10) + " GB RAM"
	}
	if mb >= 1024 {
		return strconv.FormatFloat(mb/1024, 'f', 1, 64) + " GB RAM"
	}
	return strconv.FormatFloat(mb, 'f', 0, 64) + " MB RAM"
}

// cents prints the API's cent strings ("12345") as reais.
func cents(raw string) string {
	v, ok := new(big.Int).SetString(strings.TrimSpace(raw), 10)
	if !ok {
		return orDash(raw)
	}
	return money(new(big.Int).Mul(v, big.NewInt(10_000)).String(), 2)
}

func centsPtr(raw *string) string {
	if raw == nil {
		return "—"
	}
	return cents(*raw)
}

// hoursPerMonth is the platform's billing month: an hourly rate is the
// published monthly price over 672 hours (28 days), so 672 times the rate is
// the month the console and the API quote (a longer month never bills more).
const hoursPerMonth = 672

// perMonth is "R$ 49,90/mo" from µ¢ per hour.
func perMonth(microPerHour *string) string {
	if microPerHour == nil {
		return "—"
	}
	v, ok := new(big.Int).SetString(strings.TrimSpace(*microPerHour), 10)
	if !ok {
		return "—"
	}
	return money(new(big.Int).Mul(v, big.NewInt(hoursPerMonth)).String(), 2) + "/mo"
}

func perHour(microPerHour *string) string {
	if microPerHour == nil {
		return "—"
	}
	return money(*microPerHour, 4) + "/h"
}

// rateFor is the rate of a term ("MONTHLY") in a list of rates.
func rateFor(rates []api.TermRate, term string) *string {
	for _, r := range rates {
		if strings.EqualFold(r.Term, term) {
			return r.RateMicroCentsPerHour
		}
	}
	return nil
}

// terms: what people type, and what the API calls it.
var termNames = map[string]string{
	"monthly": "MONTHLY", "month": "MONTHLY", "mensal": "MONTHLY",
	"quarterly": "QUARTERLY", "trimestral": "QUARTERLY",
	"semi-annual": "SEMI_ANNUAL", "semiannual": "SEMI_ANNUAL", "semi_annual": "SEMI_ANNUAL", "semestral": "SEMI_ANNUAL",
	"annual": "ANNUAL", "yearly": "ANNUAL", "anual": "ANNUAL",
	"on-demand": "ON_DEMAND", "ondemand": "ON_DEMAND", "on_demand": "ON_DEMAND", "hourly": "ON_DEMAND",
}

func parseTerm(s string) (string, error) {
	if t, ok := termNames[strings.ToLower(strings.TrimSpace(s))]; ok {
		return t, nil
	}
	return "", fmt.Errorf("--term is monthly, quarterly, semi-annual, annual or on-demand (not %q)", s)
}

func termText(t string) string {
	return strings.ToLower(strings.ReplaceAll(t, "_", "-"))
}

// typeToConfirm asks for something to be typed back (an id, a name) before
// an irreversible change; with nothing to read it names the flag instead.
func (a *App) typeToConfirm(question, expected, flag string) error {
	ask := a.Out
	if a.JSON {
		ask = a.Err
	}
	fmt.Fprint(ask, question)
	line, err := bufio.NewReader(a.In).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(ask)
			return fmt.Errorf("no answer: add %s to go ahead without being asked", flag)
		}
		return err
	}
	if strings.TrimSpace(line) != expected {
		return errors.New("that is not it: nothing was changed")
	}
	return nil
}

// waitFlags adds --wait and --timeout to a command whose work goes on after
// it answers.
func waitFlags(cmd *cobra.Command, wait *bool, timeout *time.Duration, def time.Duration) {
	cmd.Flags().BoolVar(wait, "wait", false, "wait until it is done")
	cmd.Flags().DurationVar(timeout, "timeout", def, "with --wait: how long to wait")
}

// waitOperation polls an operation until it ends.
func (a *App) waitOperation(ctx context.Context, id string, timeout time.Duration) (*api.Operation, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	last := ""
	for {
		var op api.Operation
		if err := c.Do(ctx, "GET", "/operations/"+id, nil, nil, &op); err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("still not done after %s: `mw operations get %s` says where it is", timeout, id)
			}
			return nil, err
		}
		line := op.Status
		if op.Progress != nil {
			line += fmt.Sprintf(" %.0f%%", *op.Progress)
		}
		if line != last && !a.JSON {
			fmt.Fprintf(a.Err, "  %s: %s\n", op.Type, line)
			last = line
		}
		if op.Terminal() {
			if op.Status != "completed" {
				msg := op.Status
				if op.Error != nil && *op.Error != "" {
					msg += ": " + *op.Error
				}
				return &op, fmt.Errorf("operation %s %s", op.ID, msg)
			}
			return &op, nil
		}
		select {
		case <-ctx.Done():
			return &op, fmt.Errorf("still not done after %s: `mw operations get %s` says where it is", timeout, id)
		case <-time.After(pollEvery):
		}
	}
}

// waitInstance polls an instance's live status until want says it is there.
func (a *App) waitInstance(ctx context.Context, id string, timeout time.Duration, what string, want func(api.InstanceStatus) bool) (*api.InstanceStatus, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	last := ""
	for {
		var st api.InstanceStatus
		err := c.Do(ctx, "GET", "/instances/"+id+"/status", map[string][]string{"fresh": {"true"}}, nil, &st)
		if err == nil {
			if want(st) {
				return &st, nil
			}
			line := st.PowerState
			if st.Busy {
				line += ", busy"
			}
			if st.DeploymentProgress != nil {
				line += fmt.Sprintf(", %.0f%%", *st.DeploymentProgress)
			}
			if line != last && !a.JSON {
				fmt.Fprintf(a.Err, "  instance %s: %s\n", id, line)
				last = line
			}
		} else {
			var p *api.Problem
			// A status read can fail while a machine is being rebuilt: keep
			// waiting through a conflict or an unavailable hypervisor.
			if !errors.As(err, &p) || (p.Status != http.StatusConflict && p.Status < 500) {
				if ctx.Err() == nil {
					return nil, err
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("instance %s is not %s after %s: `mw instances status %s` says where it is", id, what, timeout, id)
		case <-time.After(pollEvery):
		}
	}
}

// waitAccepted waits on a 202: its operation when it has one, else fallback.
func (a *App) waitAccepted(ctx context.Context, acc api.OperationAccepted, timeout time.Duration, fallback func(context.Context) error) error {
	if acc.OperationID != nil && *acc.OperationID != "" {
		_, err := a.waitOperation(ctx, *acc.OperationID, timeout)
		return err
	}
	if fallback != nil {
		return fallback(ctx)
	}
	return nil
}

// acceptedText says what a 202 means.
func acceptedText(acc api.OperationAccepted, what string) string {
	if acc.OperationID != nil && *acc.OperationID != "" {
		return fmt.Sprintf("%s: started (operation %s — `mw operations wait %s`).", what, *acc.OperationID, *acc.OperationID)
	}
	return what + ": started."
}

// routeMissing says whether a 404 is the API not having the endpoint at all
// (the router's "Cannot GET /v1/…"), rather than a resource it does not find.
func routeMissing(err error) bool {
	var p *api.Problem
	return errors.As(err, &p) && p.Status == http.StatusNotFound && strings.HasPrefix(strings.TrimSpace(p.Detail), "Cannot ")
}

// notOffered turns a 404 on an endpoint the API does not have yet into a
// sentence, and leaves every other error as it is.
func notOffered(err error, what string) error {
	if routeMissing(err) {
		return fmt.Errorf("%s is not available on this API yet", what)
	}
	return err
}
