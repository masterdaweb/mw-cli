package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/masterdaweb/mw-cli/internal/config"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// openBrowser opens a link in the default browser; replaced in tests.
var openBrowser = func(link string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", link)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", link)
	default:
		cmd = exec.Command("xdg-open", link)
	}
	return cmd.Start()
}

// canOpenBrowser: a desktop session, not SSH or a container without one.
func canOpenBrowser() bool {
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" || os.Getenv("MW_NO_BROWSER") != "" {
		return false
	}
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return false
	}
	return true
}

func loginCmd(a *App) *cobra.Command {
	var withToken, noBrowser bool
	var scopes []string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in: approve this computer in the console (or use an API token)",
		Long: "Sign in from this computer. `mw login` shows a code and opens the console, where you\n" +
			"approve it — on any device, so it works over SSH too. The CLI then gets a token of its\n" +
			"own, named after this computer and revocable under Settings → API tokens.\n\n" +
			"In CI, set MW_TOKEN instead, or save a personal access token (mwk_…) with\n" +
			"`mw login --token mwk_…` or `mw login --with-token < token.txt`.",
		Example: "  mw login\n" +
			"  mw login --no-browser\n" +
			"  mw login --scopes vm:read,dns:read     # a token that can only read\n" +
			"  mw login --with-token < token.txt",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			saved, _ := config.Load()
			base := firstNonEmpty(a.apiURL, saved.APIURL, api.DefaultBaseURL)
			token := a.token
			if withToken {
				line, err := bufio.NewReader(a.In).ReadString('\n')
				if err != nil && line == "" {
					return errors.New("no token on standard input")
				}
				token = strings.TrimSpace(line)
			}
			if token != "" {
				return a.saveToken(cmd.Context(), base, token, "token")
			}
			return a.deviceLogin(cmd, base, noBrowser, strings.Join(scopes, " "))
		},
	}
	cmd.Flags().BoolVar(&withToken, "with-token", false, "read a personal access token (mwk_…) from standard input")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print the link instead of opening the browser")
	cmd.Flags().StringSliceVar(&scopes, "scopes", nil, "ask for only these scopes, comma-separated (default: every scope your account can grant)")
	return cmd
}

// saveToken checks a token against the API and keeps it.
func (a *App) saveToken(ctx context.Context, base, token, source string) error {
	if !strings.HasPrefix(token, "mwk_") && source == "token" {
		return errors.New("that is not an API token: they start with mwk_ (create one under Settings → API tokens)")
	}
	c := api.New(base, token, a.Version)
	var me api.Me
	if err := c.Do(ctx, "GET", "/me", nil, nil, &me); err != nil {
		return err
	}
	if err := config.Save(config.Credentials{APIURL: base, Token: token, Source: source}); err != nil {
		return err
	}
	if a.JSON {
		return a.encode(me)
	}
	fmt.Fprintf(a.Out, "Signed in to account %s (%d scope(s)).\n", me.AccountID, len(me.Scopes))
	return nil
}

// deviceLogin is the OAuth device flow: a code to approve in the console.
func (a *App) deviceLogin(cmd *cobra.Command, base string, noBrowser bool, scope string) error {
	ctx := cmd.Context()
	c := api.New(base, "", a.Version)
	host, _ := os.Hostname()
	da, err := c.StartDeviceAuthorization(ctx, api.CLIClientID, scope, host)
	if err != nil {
		var p *api.Problem
		if errors.As(err, &p) && (p.Status == http.StatusNotFound || p.Status == http.StatusMethodNotAllowed) {
			// An API that does not offer it yet: paste a token instead.
			return a.pasteToken(cmd, base)
		}
		return deviceStartError(err)
	}
	// Progress and the code go to standard error: standard output stays the
	// answer (the account) for scripts.
	link := firstNonEmpty(da.VerificationURIComplete, da.VerificationURI)
	fmt.Fprintf(a.Err, "To sign in, open %s\nand confirm the code: %s\n", da.VerificationURI, da.UserCode)
	if !noBrowser && canOpenBrowser() {
		if openBrowser(link) == nil {
			fmt.Fprintln(a.Err, "(the browser should be open on that page)")
		}
	}
	fmt.Fprintln(a.Err, "Waiting for the approval…")
	interval := time.Duration(da.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	expires := time.Duration(da.ExpiresIn) * time.Second
	if expires <= 0 {
		expires = 10 * time.Minute
	}
	deadline := time.Now().Add(expires)
	for {
		if time.Now().After(deadline) {
			return errors.New("the code expired before it was approved: run `mw login` again")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(scaled(interval)):
		}
		tok, err := c.PollDeviceToken(ctx, api.CLIClientID, da.DeviceCode)
		if err == nil {
			return a.saveToken(ctx, base, tok.AccessToken, "device")
		}
		var oe *api.OAuthError
		var p *api.Problem
		switch {
		case errors.As(err, &oe):
		case errors.As(err, &p) && p.Status == http.StatusTooManyRequests:
			// A rate limit in front of the token endpoint: ask less often.
			interval += 5 * time.Second
			continue
		default:
			return err
		}
		switch oe.Code {
		case "authorization_pending":
		case "slow_down", "too_many_requests":
			// RFC 8628: every slow_down adds 5 seconds, for good.
			interval += 5 * time.Second
		case "access_denied":
			return errors.New("the sign-in was refused in the console")
		case "expired_token":
			return errors.New("the code expired before it was approved: run `mw login` again")
		case "invalid_grant":
			return errors.New("the sign-in code is no longer valid (it was used, or it is unknown): run `mw login` again")
		default:
			return err
		}
	}
}

// deviceStartError says why a sign-in could not start, and what to do.
func deviceStartError(err error) error {
	var oe *api.OAuthError
	var p *api.Problem
	switch {
	case errors.As(err, &oe) && (oe.Code == "too_many_requests" || oe.Status == http.StatusTooManyRequests),
		errors.As(err, &p) && p.Status == http.StatusTooManyRequests:
		return errors.New("too many sign-ins were started from this address: wait a few minutes and run `mw login` again (or sign in with a token: `mw login --with-token`)")
	case errors.As(err, &oe) && oe.Code == "invalid_scope":
		return fmt.Errorf("the API refused the scopes asked for: %s Leave out --scopes to ask for every scope your account can grant", strings.TrimSpace(firstNonEmpty(oe.Description, "one of them is unknown.")))
	case errors.As(err, &oe) && oe.Code == "invalid_client":
		return errors.New("this API does not accept sign-ins from mw: sign in with a token instead (`mw login --with-token`)")
	}
	return err
}

// scaled is a wait, shortened in tests.
func scaled(d time.Duration) time.Duration {
	if pollEvery < time.Second {
		return pollEvery
	}
	return d
}

// pasteToken is the old way in: a personal access token typed or pasted.
func (a *App) pasteToken(cmd *cobra.Command, base string) error {
	fmt.Fprintln(a.Err, "This API does not offer signing in from the browser yet: paste an API token instead.")
	fmt.Fprint(a.Err, "API token (mwk_…, from Settings → API tokens): ")
	var line string
	if f, ok := a.In.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		raw, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(a.Err)
		if err != nil {
			return err
		}
		line = string(raw)
	} else {
		l, err := bufio.NewReader(a.In).ReadString('\n')
		if err != nil && l == "" {
			return errors.New("no token given")
		}
		line = l
	}
	return a.saveToken(cmd.Context(), base, strings.TrimSpace(line), "token")
}

func logoutCmd(a *App) *cobra.Command {
	var revoke, keep bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Sign out: revoke this computer's token and forget it",
		Long: "Sign out. A token this computer got from `mw login` is revoked as well — it stops\n" +
			"working everywhere at once. A personal access token you pasted is only forgotten here\n" +
			"(it may be in use elsewhere, a CI job say): add --revoke to revoke it too.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			saved, err := config.Load()
			if err != nil {
				return err
			}
			revoked := false
			if saved.Token != "" && !keep && (saved.Source == "device" || revoke) {
				base := firstNonEmpty(a.apiURL, saved.APIURL, api.DefaultBaseURL)
				c := api.New(base, saved.Token, a.Version)
				err := c.Do(cmd.Context(), "DELETE", "/me/token", nil, nil, nil)
				switch {
				case err == nil:
					revoked = true
				case errors.Is(err, api.ErrUnauthorized):
					revoked = true // already revoked
				default:
					fmt.Fprintf(a.Err, "Could not revoke the token (%s): revoke it under Settings → API tokens.\n", Describe(err))
				}
			}
			if err := config.Remove(); err != nil {
				return err
			}
			if a.JSON {
				return a.encode(map[string]interface{}{"signed_out": true, "revoked": revoked})
			}
			if revoked {
				fmt.Fprintln(a.Out, "Signed out, and the token revoked.")
			} else {
				fmt.Fprintln(a.Out, "Signed out. The token is still valid: revoke it under Settings → API tokens if it should stop working.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&revoke, "revoke", false, "revoke a pasted personal access token too")
	cmd.Flags().BoolVar(&keep, "keep-token", false, "only forget the token here, never revoke it")
	return cmd
}

func whoamiCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "The account and scopes of the token in use",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var me api.Me
			if err := c.Do(cmd.Context(), "GET", "/me", nil, nil, &me); err != nil {
				return err
			}
			return a.print(me, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Account\t%s\n", me.AccountID)
				fmt.Fprintf(w, "API\t%s\n", c.BaseURL)
				if me.TokenID != "" {
					fmt.Fprintf(w, "Token\t%s\n", me.TokenID)
				}
				fmt.Fprintf(w, "Scopes\t%s\n", strings.Join(me.Scopes, " "))
			})
		},
	}
}
