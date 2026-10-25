package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/coder/websocket"
	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// `mw run`: a command, or a shell, in a new container of a service's live
// revision, this terminal attached to it over a WebSocket to the platform.
// Binary messages carry the input and the output; text messages are control
// — the terminal's size and the end of piped input out, the exit code (or
// why it could not start) in.

func runCmd(a *App) *cobra.Command {
	var service, project string
	var noTTY bool
	cmd := &cobra.Command{
		Use:   "run [--service <name>] [-- command...]",
		Short: "Run a command, or open a shell, in a service's live revision",
		Long: "Start a new container of the service's live revision — its image, its variables (secrets\n" +
			"included), its size — with this terminal attached: a shell when no command is given. It takes\n" +
			"no traffic, is billed by the second while it runs and ends with the command (after an hour at\n" +
			"the latest). mw run exits with the command's exit code, so CI can run a migration with it.\n" +
			"The token needs the serverless:run scope.",
		Example: "  mw run\n  mw run -- php artisan tinker\n  mw run --service worker -- node scripts/backfill.js\n" +
			"  mw run -- php artisan migrate --force\n  mw run --no-tty -- 'psql \"$DATABASE_URL\"' < consulta.sql",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := a.resolveService(ctx, service, project)
			if err != nil {
				return err
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			inFd, outFd := fdOf(a.In), fdOf(a.Out)
			tty := !noTTY && inFd >= 0 && outFd >= 0 && term.IsTerminal(inFd) && term.IsTerminal(outFd)
			command := shellCommand(args)
			var started api.StartedRun
			body := map[string]interface{}{"tty": tty}
			if command != "" {
				body["command"] = command
			}
			if err := c.Do(ctx, "POST", "/serverless/services/"+url.PathEscape(s.ID)+"/runs", nil, body, &started); err != nil {
				return err
			}
			what := "a shell"
			if command != "" {
				what = command
			}
			fmt.Fprintf(a.Err, "Running %s in %s…\n", what, s.Name)
			return a.attachRun(ctx, started.AttachURL, tty, inFd, outFd)
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "the service (default: the one this directory is linked to)")
	cmd.Flags().StringVar(&project, "project", "", "the project, when the service's name is in more than one")
	cmd.Flags().BoolVar(&noTTY, "no-tty", false, "no terminal: input and output as they are (the default when not in a terminal)")
	return cmd
}

// shellCommand is what the service's shell runs: one argument as it is (it
// may hold pipes and quotes of its own), several quoted one by one.
func shellCommand(args []string) string {
	if len(args) == 1 {
		return strings.TrimSpace(args[0])
	}
	quoted := make([]string, len(args))
	for i, s := range args {
		quoted[i] = shellQuote(s)
	}
	return strings.Join(quoted, " ")
}

// shellQuote leaves a plain word alone and single-quotes anything else.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./:=@%+,") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fdOf is the file descriptor behind a stream, or -1 when it is not a file.
func fdOf(v interface{}) int {
	if f, ok := v.(*os.File); ok {
		return int(f.Fd())
	}
	return -1
}

// runControl is a text message of the attach protocol.
type runControl struct {
	Type    string           `json:"type"`
	Code    *json.RawMessage `json:"code,omitempty"`
	Message string           `json:"message,omitempty"`
	Cols    int              `json:"cols,omitempty"`
	Rows    int              `json:"rows,omitempty"`
}

// attachFailures are the platform's reasons a run could not be attached.
var attachFailures = map[string]string{
	"expired":  "the link to the command expired before it connected: run it again",
	"no_shell": "the service's image has no shell (/bin/sh), so commands cannot run in it",
	"image":    "the service's image did not start: see `mw deployments`",
	"timeout":  "the container did not start in time: try again in a moment",
	"ended":    "the command ended before this terminal attached",
}

func (a *App) attachRun(ctx context.Context, attachURL string, tty bool, inFd, outFd int) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ws, res, err := websocket.Dial(ctx, attachURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"User-Agent": []string{"mw-cli/" + a.Version}},
	})
	if err != nil {
		if res != nil {
			return fmt.Errorf("the platform refused the terminal (%s)", res.Status)
		}
		return fmt.Errorf("reaching the platform: %w", err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(16 << 20)

	send := func(c runControl) {
		b, _ := json.Marshal(c)
		_ = ws.Write(ctx, websocket.MessageText, b)
	}
	if tty {
		old, err := term.MakeRaw(inFd)
		if err != nil {
			return fmt.Errorf("this terminal: %w", err)
		}
		defer term.Restore(inFd, old)
		if cols, rows, err := term.GetSize(outFd); err == nil {
			send(runControl{Type: "resize", Cols: cols, Rows: rows})
		}
		stop := watchResize(outFd, func(cols, rows int) { send(runControl{Type: "resize", Cols: cols, Rows: rows}) })
		defer stop()
	}
	// Without a terminal, Ctrl-C cancels ctx (Main's) and so ends the
	// command; in one, it is a key like any other, for the command to read.

	// The input: as it is typed (or piped), then its end.
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := a.In.Read(buf)
			if n > 0 {
				if ws.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) && !tty {
					send(runControl{Type: "eof"})
				}
				return
			}
		}
	}()

	for {
		kind, data, err := ws.Read(ctx)
		if err != nil {
			if ctx.Err() != nil || websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				// Interrupted here, or closed by the platform without a word.
				return &exitError{code: 130, msg: "the command was ended"}
			}
			return fmt.Errorf("the connection to the command dropped: %w", err)
		}
		if kind == websocket.MessageBinary {
			if _, err := a.Out.Write(data); err != nil {
				return err
			}
			continue
		}
		var c runControl
		if json.Unmarshal(data, &c) != nil {
			continue
		}
		switch c.Type {
		case "exit":
			var code int
			if c.Code == nil || json.Unmarshal(*c.Code, &code) != nil {
				return nil
			}
			if code != 0 {
				return &exitError{code: code}
			}
			return nil
		case "error":
			var reason string
			if c.Code != nil {
				_ = json.Unmarshal(*c.Code, &reason)
			}
			if msg, ok := attachFailures[reason]; ok {
				return errors.New(msg)
			}
			if c.Message != "" {
				return errors.New(c.Message)
			}
			return errors.New("the command could not start")
		}
	}
}
