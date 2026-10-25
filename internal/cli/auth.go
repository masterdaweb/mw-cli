package cli

import (
	"bufio"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/masterdaweb/mw-cli/internal/config"
	"github.com/spf13/cobra"
)

func loginCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Sign in with an API token from the console (Settings → API tokens)",
		Long: "Paste a personal access token (mwk_…). It is checked against the API and kept in your\n" +
			"user configuration, readable by you alone. In CI, set MW_TOKEN instead.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			token := a.token
			if token == "" {
				fmt.Fprint(a.Err, "API token (mwk_…): ")
				line, err := bufio.NewReader(a.In).ReadString('\n')
				if err != nil && line == "" {
					return errors.New("no token given")
				}
				token = strings.TrimSpace(line)
			}
			if !strings.HasPrefix(token, "mwk_") {
				return errors.New("that is not an API token: they start with mwk_ (create one under Settings → API tokens)")
			}
			saved, _ := config.Load()
			base := firstNonEmpty(a.apiURL, saved.APIURL, api.DefaultBaseURL)
			c := api.New(base, token, a.Version)
			var me api.Me
			if err := c.Do(cmd.Context(), "GET", "/me", nil, nil, &me); err != nil {
				return err
			}
			if err := config.Save(config.Credentials{APIURL: base, Token: token}); err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "Signed in to account %s (%d scope(s)).\n", me.AccountID, len(me.Scopes))
			return nil
		},
	}
}

func logoutCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget the saved token (it stays valid until you revoke it in the console)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := config.Remove(); err != nil {
				return err
			}
			fmt.Fprintln(a.Out, "Signed out. Revoke the token under Settings → API tokens if it should stop working.")
			return nil
		},
	}
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
				fmt.Fprintf(w, "Scopes\t%s\n", strings.Join(me.Scopes, " "))
			})
		},
	}
}
