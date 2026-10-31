package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// A database is reached from this computer through the platform: a port on
// 127.0.0.1 whose every connection the platform carries to the database
// over a WebSocket of its own (a tunnel, once per connection), so the
// database need not allow this computer's address. TLS stays end to end,
// between the client and the database, verified against the database's own
// name. --direct connects to its address instead (it must allow yours).

// dbRoute is how a client reaches the database: the name TLS verifies, and
// where to connect — the database itself, or the tunnel's local port.
type dbRoute struct {
	// host is the database's name, what its certificate carries.
	host string
	// addr and port are where to connect: host itself when direct, else 127.0.0.1.
	addr   string
	port   int
	tunnel bool
	// fingerprint of the database's certificate (through a tunnel), to pin.
	fingerprint string
	close       func()
}

// routeTo opens the way to a database: the tunnel unless a.dbDirect.
func (a *App) routeTo(ctx context.Context, d *api.Database, pooler bool) (*dbRoute, error) {
	conn := d.Connection
	port := conn.Port
	if pooler && conn.PoolerPort != nil {
		port = *conn.PoolerPort
	}
	if a.dbDirect {
		return &dbRoute{host: conn.Host, addr: conn.Host, port: port, close: func() {}}, nil
	}
	return a.listenTunnel(ctx, d, pooler, 0)
}

// listenTunnel opens 127.0.0.1:port (0: any free one) and carries each
// connection made to it through a tunnel of its own.
func (a *App) listenTunnel(ctx context.Context, d *api.Database, pooler bool, port int) (*dbRoute, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	open := func(ctx context.Context) (*api.DatabaseTunnel, error) {
		var t api.DatabaseTunnel
		if err := c.Do(ctx, "POST", "/databases/"+url.PathEscape(d.Name)+"/tunnel", nil, map[string]bool{"pooler": pooler}, &t); err != nil {
			return nil, err
		}
		return &t, nil
	}
	// The first one now: it says whether the database takes one at all,
	// and serves the first connection.
	first, err := open(ctx)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
	if err != nil {
		return nil, fmt.Errorf("port %d on this computer: %w", port, err)
	}
	ctx, cancel := context.WithCancel(ctx)
	var mu sync.Mutex
	spare := first
	take := func() *api.DatabaseTunnel {
		mu.Lock()
		defer mu.Unlock()
		t := spare
		spare = nil
		if t != nil {
			if exp, err := time.Parse(time.RFC3339, t.ExpiresAt); err == nil && time.Until(exp) < 5*time.Second {
				return nil
			}
		}
		return t
	}
	go func() {
		for {
			local, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer local.Close()
				t := take()
				if t == nil {
					var err error
					if t, err = open(ctx); err != nil {
						fmt.Fprintf(a.Err, "mw: a connection to %s was refused: %s\n", d.Name, Describe(err))
						return
					}
				}
				if err := a.carry(ctx, local, t.URL); err != nil && ctx.Err() == nil {
					fmt.Fprintf(a.Err, "mw: the tunnel to %s: %v\n", d.Name, err)
				}
			}()
		}
	}()
	fingerprint := ""
	if first.FingerprintSHA256 != nil {
		fingerprint = *first.FingerprintSHA256
	}
	return &dbRoute{
		host:        first.Hostname,
		addr:        "127.0.0.1",
		port:        ln.Addr().(*net.TCPAddr).Port,
		tunnel:      true,
		fingerprint: fingerprint,
		close: func() {
			cancel()
			ln.Close()
		},
	}, nil
}

// carry moves one connection's bytes through a tunnel, both ways, until
// either side hangs up.
func (a *App) carry(ctx context.Context, local net.Conn, tunnelURL string) error {
	ws, res, err := websocket.Dial(ctx, tunnelURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"User-Agent": []string{"mw-cli/" + a.Version}},
	})
	if err != nil {
		if res != nil {
			return fmt.Errorf("refused (%s)", res.Status)
		}
		return err
	}
	ws.SetReadLimit(16 << 20)
	remote := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	defer remote.Close()
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(remote, local)
		done <- err
	}()
	_, err = io.Copy(local, remote)
	if err == nil || errors.Is(err, io.EOF) || websocket.CloseStatus(err) == websocket.StatusNormalClosure {
		return nil
	}
	if websocket.CloseStatus(err) == websocket.StatusPolicyViolation {
		return errors.New("its link had expired or was used")
	}
	select {
	case <-done:
		return nil
	default:
		return err
	}
}

func dbTunnelCmd(a *App, engine string) *cobra.Command {
	var port int
	var pooler bool
	cmd := &cobra.Command{
		Use:   "tunnel <name>",
		Short: "Keep a port on this computer open to a database, through the platform",
		Long: "Open 127.0.0.1:<port> to the database for any tool — a GUI, an ORM, a script — whatever\n" +
			"the addresses the database allows: each connection is carried by the platform. The\n" +
			"database still asks for its password, and TLS stays between the tool and the database:\n" +
			"verify its certificate against the database's name, which this prints. Ctrl-C closes it.",
		Example: "  mw db tunnel loja-prod --port 15432\n  mw db tunnel loja-wp --port 13306",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := a.database(cmd.Context(), args[0], engine)
			if err != nil {
				return err
			}
			if d.Status != "running" && d.Status != "updating" {
				return fmt.Errorf("%s is %s: it takes connections once running", d.Name, dbStatus(d.Status))
			}
			r, err := a.listenTunnel(cmd.Context(), d, pooler, port)
			if err != nil {
				return err
			}
			defer r.close()
			conn := d.Connection
			// A private environment's certificates chain to a CA of its own.
			var ca api.DatabaseCA
			if c, err := a.Client(); err == nil {
				_ = c.Do(cmd.Context(), "GET", "/databases/ca-certificate", nil, nil, &ca)
			}
			roots, mysqlCA := "system", ""
			if ca.CACertificate != nil {
				roots, mysqlCA = "ca.pem", " --ssl-ca=ca.pem"
			}
			fmt.Fprintf(a.Out, "127.0.0.1:%d → %s (%s), through the platform. Ctrl-C to close it.\n", r.port, d.Name, engineName(d.Engine))
			fmt.Fprintf(a.Out, "  User %s; the password: mw db password %s\n", conn.Username, d.Name)
			if ca.CACertificate != nil {
				fmt.Fprintf(a.Out, "  The CA its certificate chains to: mw db ca > ca.pem\n")
			}
			switch {
			case d.Engine == "postgres":
				// libpq checks the certificate against host while connecting to hostaddr.
				fmt.Fprintf(a.Out, "  psql \"host=%s hostaddr=127.0.0.1 port=%d dbname=%s user=%s sslmode=verify-full sslrootcert=%s\"\n",
					r.host, r.port, str(conn.Database), conn.Username, roots)
			case d.Engine == "mariadb" && r.fingerprint != "":
				// MariaDB's client checks the name whenever it has a CA: pin the certificate instead.
				fmt.Fprintf(a.Out, "  mariadb -h 127.0.0.1 -P %d -u %s --ssl-fp=%s -p %s\n", r.port, conn.Username, r.fingerprint, str(conn.Database))
			case isSQLServer(d.Engine):
				// sqlcmd (Microsoft's ODBC one or the Go one) checks the certificate against -F while connecting to 127.0.0.1.
				fmt.Fprintf(a.Out, "  sqlcmd -S tcp:127.0.0.1,%d -U %s -d %s -N -F %s\n", r.port, conn.Username, str(conn.Database), r.host)
			case isMySQL(d.Engine):
				fmt.Fprintf(a.Out, "  mysql -h 127.0.0.1 -P %d -u %s --ssl-mode=VERIFY_CA%s -p %s\n", r.port, conn.Username, mysqlCA, str(conn.Database))
			default:
				fmt.Fprintf(a.Out, "  redis-cli --tls --sni %s -h 127.0.0.1 -p %d --user %s --askpass\n", r.host, r.port, conn.Username)
			}
			fmt.Fprintf(a.Out, "  Other tools: host 127.0.0.1, port %d, SSL required. The certificate is for %s:\n"+
				"  to check its name too, point %s at 127.0.0.1 in your hosts file and use that name.\n", r.port, r.host, r.host)
			<-cmd.Context().Done()
			return nil
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "the port on 127.0.0.1 (default: any free one)")
	if engine != "valkey" {
		cmd.Flags().BoolVar(&pooler, "pooler", false, "PostgreSQL: through the pooler instead of the direct port")
	}
	return cmd
}
