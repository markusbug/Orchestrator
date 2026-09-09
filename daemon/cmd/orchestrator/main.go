// Command orchestrator runs the Orchestrator host daemon and its CLI.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/mdp/qrterminal/v3"

	"github.com/markusbug/Orchestrator/daemon/internal/admin"
	"github.com/markusbug/Orchestrator/daemon/internal/api"
	"github.com/markusbug/Orchestrator/daemon/internal/auth"
	"github.com/markusbug/Orchestrator/daemon/internal/claude"
	"github.com/markusbug/Orchestrator/daemon/internal/config"
	"github.com/markusbug/Orchestrator/daemon/internal/core"
	"github.com/markusbug/Orchestrator/daemon/internal/protocol"
	"github.com/markusbug/Orchestrator/daemon/internal/relay/client"
	"github.com/markusbug/Orchestrator/daemon/internal/relay/vconn"
	"github.com/markusbug/Orchestrator/daemon/internal/relay/wire"
	"github.com/markusbug/Orchestrator/daemon/internal/service"
)

const usage = `Orchestrator - run Claude Code sessions on this machine, drive them from your phone.

Usage: orchestrator [command] [flags]

Run with no arguments to open the Orchestrator app, which is how setup,
pairing and device management are meant to be done.

Commands:
  serve       run the daemon in the foreground
  install     install and start the daemon as a user service
  uninstall   stop and remove the user service
  version     print version

Flags for serve:
  --debug        serve the browser debug client at /_debug and skip auth on loopback
  --port N       listen port (default 7391 or config)
  --bind ADDR    bind address (default all interfaces)
  --dir PATH     config directory (default ~/.config/orchestrator)

The relay lets phones reach this machine from anywhere without any port or
firewall setup: the daemon connects out, so nothing needs to listen. It is
end-to-end encrypted with this machine's own certificate (see docs/RELAY.md).
`

func main() {
	// The app is the supported way in, so bare `orchestrator` opens it. The
	// commands below still work for headless machines and for support, they
	// are just no longer the documented path.
	if len(os.Args) < 2 {
		if err := launchApp(); err != nil {
			fmt.Fprintln(os.Stderr, "could not open the Orchestrator app:", err)
			fmt.Fprint(os.Stderr, "\n", usage)
			os.Exit(2)
		}
		return
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "serve":
		err = runServe(args)
	case "install":
		err = runInstall(args)
	case "uninstall":
		err = service.Uninstall("")
		if err == nil {
			fmt.Println("service removed")
		}
	case "status":
		err = runStatus(args)
	case "pair":
		err = runPair(args)
	case "devices":
		err = runDevices(args)
	case "sessions":
		err = runSessions(args)
	case "relay":
		err = runRelay(args)
	case "logs":
		err = runLogs()
	case "version", "--version", "-v":
		fmt.Println("orchestrator", core.Version)
	case "_hook":
		err = runHook(args)
	case "_paths":
		err = runPaths(args)
	case "_service":
		err = runService(args)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func paths(dir string) (config.Paths, error) { return config.DefaultPaths(dir) }

// parseArgs parses flags that may appear before or after positional
// arguments (so `sessions kill ID --dir X` works) and returns the positionals.
func parseArgs(fs *flag.FlagSet, args []string) []string {
	var positional []string
	rest := args
	for len(rest) > 0 {
		if strings.HasPrefix(rest[0], "-") && rest[0] != "-" {
			fs.Parse(rest)
			rest = fs.Args()
			continue
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}
	return positional
}

func adminClient(dir string) (*admin.Client, error) {
	p, err := paths(dir)
	if err != nil {
		return nil, err
	}
	return admin.NewClient(p.AdminSocket), nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	debug := fs.Bool("debug", false, "enable debug web client and loopback auto-auth")
	port := fs.Int("port", 0, "listen port")
	bind := fs.String("bind", "", "bind address")
	dir := fs.String("dir", "", "config directory")
	verbose := fs.Bool("v", false, "verbose logging")
	parseArgs(fs, args)

	p, err := paths(*dir)
	if err != nil {
		return err
	}
	cfg, err := config.Load(p)
	if err != nil {
		return err
	}
	if *port != 0 {
		cfg.Port = *port
	}
	if *bind != "" {
		cfg.Bind = *bind
	}
	dbg := *debug || cfg.Debug
	level := slog.LevelInfo
	if *verbose || dbg {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	c, err := core.Open(ctx, p, cfg, dbg, log)
	if err != nil {
		return err
	}
	defer c.Close()

	log.Info("orchestrator starting", "version", core.Version, "port", cfg.Port, "bind", cfg.Bind, "dir", p.Dir, "debug", dbg)
	log.Info("tls fingerprint", "fp", c.Identity.Fingerprint)
	if dbg {
		log.Warn("debug mode: loopback connections are auto-authenticated", "url", fmt.Sprintf("https://localhost:%d/_debug/", cfg.Port))
	}
	// The relay is an extra path, never a prerequisite: a broken relay
	// setup is logged and the daemon still serves LAN and Tailscale.
	var extra []net.Listener
	if !cfg.Relay.Active() {
		log.Info("relay off; phones can only reach this machine directly", "host_id", c.HostID)
	} else if err := cfg.Relay.Validate(dbg); err != nil {
		c.Cfg.Relay.Enabled = false
		log.Error("relay disabled: bad configuration (fix with `orchestrator relay set <url>` or edit config.toml)", "err", err)
	} else if rc, ln, err := startRelay(ctx, c, dbg, log); err != nil {
		c.Cfg.Relay.Enabled = false
		log.Error("relay disabled: cannot start", "err", err)
	} else {
		c.Relay = rc
		extra = append(extra, ln)
		log.Info("relay", "url", cfg.Relay.URL, "addr", rc.Addr(), "port", rc.Port())
	}
	for _, a := range c.Addrs() {
		log.Info("address", "ip", a.IP, "port", a.Port, "kind", a.Kind)
	}

	// The admin socket answers `orchestrator status` with c.Relay, so it
	// opens only once the relay decision is made.
	adm := &admin.Server{Core: c}
	if err := adm.Listen(p.AdminSocket); err != nil {
		return fmt.Errorf("admin socket: %w", err)
	}
	defer adm.Close()

	srv := &api.Server{Core: c, Log: log}
	if err := srv.ListenAndServe(ctx, extra...); err != nil {
		return err
	}
	log.Info("shutting down")
	return nil
}

// startRelay connects to the configured relay and returns the listener that
// receives phone connections through it.
func startRelay(ctx context.Context, c *core.Core, insecure bool, log *slog.Logger) (*client.Client, *vconn.Listener, error) {
	var tlsCfg *tls.Config
	if ca := c.Cfg.Relay.CAFile; ca != "" {
		pem, err := os.ReadFile(ca)
		if err != nil {
			return nil, nil, fmt.Errorf("relay ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, nil, fmt.Errorf("relay ca_file: no certificates in %s", ca)
		}
		tlsCfg = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	ln := vconn.NewListener("relay")
	rc, err := client.New(client.Options{
		URL: c.Cfg.Relay.URL, HostKey: c.HostKey, Version: core.Version, Listener: ln,
		TLS: tlsCfg, Insecure: insecure, Log: log,
	})
	if err != nil {
		return nil, nil, err
	}
	go rc.Run(ctx)
	return rc, ln, nil
}

func runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	path := fs.String("path", "", "PATH to embed in the service (default: the login shell's)")
	asJSON := fs.Bool("json", false, "print JSON")
	parseArgs(fs, args)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Not os.Getenv("PATH"): the desktop app installs the service too, and a
	// GUI process inherits the desktop session's PATH, which may not have
	// claude in it. See service.LoginPath.
	p := *path
	if p == "" {
		p = service.LoginPath()
	}
	unit, err := service.Install(service.Options{Exe: exe, Path: p})
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"unit": unit, "path": p})
	}
	fmt.Println("installed", unit)
	fmt.Println("service started; open the Orchestrator app to pair a phone")
	return nil
}

func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dir := fs.String("dir", "", "config directory")
	asJSON := fs.Bool("json", false, "print JSON")
	parseArgs(fs, args)
	cl, err := adminClient(*dir)
	if err != nil {
		return err
	}
	st, err := cl.Status()
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(st)
	}
	fmt.Printf("orchestrator %s running (pid %d, up %s)\n", st.Version, st.PID, (time.Duration(st.UptimeSec) * time.Second).String())
	fmt.Printf("host:        %s\n", st.Host)
	fmt.Printf("port:        %d (bind %q)\n", st.Port, st.Bind)
	fmt.Printf("fingerprint: %s\n", st.Fingerprint)
	fmt.Printf("config:      %s\n", st.ConfigDir)
	for _, a := range st.Addrs {
		fmt.Printf("address:     %s (%s)\n", formatAddr(a, st.Port), a.Kind)
	}
	fmt.Printf("sessions:    %d\n", st.Sessions)
	fmt.Printf("devices:     %d\n", st.Devices)
	printRelay(st.Relay)
	if st.Debug {
		fmt.Println("debug:       on (loopback auto-auth, /_debug enabled)")
	}
	return nil
}

func runPair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	dir := fs.String("dir", "", "config directory")
	asJSON := fs.Bool("json", false, "print JSON")
	parseArgs(fs, args)
	cl, err := adminClient(*dir)
	if err != nil {
		return err
	}
	p, err := cl.Pair()
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(p)
	uri := "orchestrator://pair?d=" + base64.RawURLEncoding.EncodeToString(raw)
	if *asJSON {
		// The app renders its own QR, so it needs the URI the QR encodes
		// rather than the terminal drawing below.
		return json.NewEncoder(os.Stdout).Encode(struct {
			protocol.PairPayload
			URI string `json:"uri"`
		}{p, uri})
	}
	fmt.Println()
	qrterminal.GenerateWithConfig(uri, qrterminal.Config{Level: qrterminal.L, Writer: os.Stdout, HalfBlocks: true, BlackChar: qrterminal.BLACK_BLACK, WhiteChar: qrterminal.WHITE_WHITE, BlackWhiteChar: qrterminal.BLACK_WHITE, WhiteBlackChar: qrterminal.WHITE_BLACK, QuietZone: 2})
	fmt.Println()
	fmt.Printf("Scan with the Orchestrator app, or enter one address manually (address:port):\n")
	fmt.Printf("  code:        %s   (expires %s)\n", p.Code, time.Unix(p.ExpiresAt, 0).Format(time.Kitchen))
	fmt.Printf("  fingerprint: %s\n", p.FP)
	if len(p.Addrs) == 0 {
		fmt.Println("  address:     (no network address found)")
	}
	for _, a := range p.Addrs {
		fmt.Printf("  address:     %s (%s)\n", formatAddr(a, p.Port), a.Kind)
	}
	fmt.Println()
	// The relay address is advertised from config; whether it works right
	// now is a different question, so answer it from the live status.
	var relay *client.Status
	if st, err := cl.Status(); err == nil {
		relay = st.Relay
	}
	switch {
	case !hasRelay(p.Addrs) || relay == nil:
		fmt.Println("The phone must be on the same network as this machine (or on the same Tailscale network).")
	case relay.Connected:
		fmt.Println("The phone can be anywhere: it reaches this machine through the relay, or directly on the same network.")
	default:
		fmt.Println("The phone must be on the same network as this machine (or on the same Tailscale network)")
		fmt.Printf("until the relay connects. Relay %s: %s\n", relay.URL, orString(relay.LastError, "still connecting"))
	}
	return nil
}

func hasRelay(addrs []protocol.HostAddr) bool {
	for _, a := range addrs {
		if a.Kind == protocol.AddrRelay {
			return true
		}
	}
	return false
}

// formatAddr renders an address with its port (falling back to the host's).
func formatAddr(a protocol.HostAddr, hostPort int) string {
	port := a.Port
	if port == 0 {
		port = hostPort
	}
	return net.JoinHostPort(a.IP, fmt.Sprint(port))
}

func orString(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func printRelay(rs *client.Status) {
	if rs == nil {
		fmt.Println("relay:       off")
		return
	}
	state := "connecting"
	if rs.Connected {
		state = "connected since " + rs.Since.Format(time.Kitchen)
	} else if rs.LastError != "" {
		state = "disconnected (" + rs.LastError + ")"
	}
	fmt.Printf("relay:       %s (%s)\n", rs.URL, state)
	fmt.Printf("relay addr:  %s:%d\n", rs.Addr, rs.Port)
	if rs.Streams > 0 {
		fmt.Printf("relay use:   %d phone connection(s)\n", rs.Streams)
	}
}

// runRelay shows or changes the relay configuration.
func runRelay(args []string) error {
	fs := flag.NewFlagSet("relay", flag.ExitOnError)
	dir := fs.String("dir", "", "config directory")
	asJSON := fs.Bool("json", false, "print JSON")
	rest := parseArgs(fs, args)
	p, err := paths(*dir)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		switch rest[0] {
		case "set":
			if len(rest) < 2 {
				return fmt.Errorf("usage: orchestrator relay set <https://relay.example>")
			}
			rc := config.RelayConfig{URL: rest[1]}
			if err := rc.Validate(false); err != nil {
				return err
			}
			if err := config.SetRelay(p, true, rest[1]); err != nil {
				return err
			}
			fmt.Println("relay set to", rest[1])
			fmt.Println("restart the daemon to apply (systemctl --user restart orchestrator, or restart `orchestrator serve`)")
			return nil
		case "off":
			if err := config.SetRelay(p, false, ""); err != nil {
				return err
			}
			fmt.Println("relay disabled; restart the daemon to apply")
			return nil
		case "on":
			if err := config.SetRelay(p, true, ""); err != nil {
				return err
			}
			fmt.Println("relay enabled; restart the daemon to apply")
			return nil
		case "status":
		default:
			return fmt.Errorf("usage: orchestrator relay [status | set <url> | on | off]")
		}
	}
	cfg, err := config.Load(p)
	if err != nil {
		return err
	}
	cl := admin.NewClient(p.AdminSocket)
	if *asJSON {
		out := map[string]any{"enabled": cfg.Relay.Enabled, "url": cfg.Relay.URL, "daemon_running": false}
		if st, err := cl.Status(); err == nil {
			out["daemon_running"], out["host_id"], out["relay"] = true, st.HostID, st.Relay
		} else if key, err := auth.LoadHostKey(p.HostKeyFile); err == nil {
			out["host_id"] = wire.HostID(key.Public().(ed25519.PublicKey))
		}
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	if st, err := cl.Status(); err == nil {
		fmt.Printf("daemon:      running (pid %d)\n", st.PID)
		fmt.Printf("host id:     %s\n", st.HostID)
		printRelay(st.Relay)
		return nil
	}
	// Daemon not running: report from the config and key file. This is a
	// status query, so it must not create the key (the daemon does that).
	fmt.Println("daemon:      not running")
	id := ""
	switch key, err := auth.LoadHostKey(p.HostKeyFile); {
	case err == nil:
		id = wire.HostID(key.Public().(ed25519.PublicKey))
		fmt.Printf("host id:     %s\n", id)
	case errors.Is(err, os.ErrNotExist):
		fmt.Println("host id:     (none yet; the daemon creates the host key on first start)")
	default:
		return err
	}
	switch {
	case !cfg.Relay.Enabled:
		fmt.Println("relay:       off (enable with `orchestrator relay on`)")
	case cfg.Relay.URL == "":
		fmt.Println("relay:       no relay configured (set one with `orchestrator relay set <url>`)")
	default:
		fmt.Printf("relay:       %s (configured)\n", cfg.Relay.URL)
		if id != "" {
			fmt.Printf("relay addr:  %s:%d\n", wire.Addr(id, cfg.Relay.Domain()), cfg.Relay.Port())
		}
	}
	return nil
}

func runDevices(args []string) error {
	fs := flag.NewFlagSet("devices", flag.ExitOnError)
	dir := fs.String("dir", "", "config directory")
	asJSON := fs.Bool("json", false, "print JSON")
	rest := parseArgs(fs, args)
	cl, err := adminClient(*dir)
	if err != nil {
		return err
	}
	if len(rest) >= 2 && rest[0] == "revoke" {
		if err := cl.RevokeDevice(rest[1]); err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"revoked": rest[1]})
		}
		fmt.Println("revoked", rest[1])
		return nil
	}
	devs, err := cl.Devices()
	if err != nil {
		return err
	}
	if *asJSON {
		if devs == nil {
			devs = []admin.DeviceInfo{}
		}
		return json.NewEncoder(os.Stdout).Encode(devs)
	}
	if len(devs) == 0 {
		fmt.Println("no paired devices; run `orchestrator pair`")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tPAIRED\tLAST SEEN\tSTATE")
	for _, d := range devs {
		state := "active"
		if d.Revoked {
			state = "revoked"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.ID, d.Name, ago(d.CreatedAt), ago(d.LastSeenAt), state)
	}
	return w.Flush()
}

func runSessions(args []string) error {
	fs := flag.NewFlagSet("sessions", flag.ExitOnError)
	dir := fs.String("dir", "", "config directory")
	asJSON := fs.Bool("json", false, "print JSON")
	rest := parseArgs(fs, args)
	cl, err := adminClient(*dir)
	if err != nil {
		return err
	}
	if len(rest) >= 2 {
		switch rest[0] {
		case "kill":
			sig := "TERM"
			if len(rest) >= 3 {
				sig = rest[2]
			}
			if err := cl.KillSession(rest[1], sig); err != nil {
				return err
			}
			fmt.Println("signalled", rest[1])
			return nil
		case "remove", "rm":
			if err := cl.RemoveSession(rest[1]); err != nil {
				return err
			}
			fmt.Println("removed", rest[1])
			return nil
		}
	}
	list, err := cl.Sessions()
	if err != nil {
		return err
	}
	if *asJSON {
		if list == nil {
			list = []protocol.SessionInfo{}
		}
		return json.NewEncoder(os.Stdout).Encode(list)
	}
	if len(list) == 0 {
		fmt.Println("no sessions")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSTATUS\tPID\tFOLDER\tCOMMAND\tLAST OUTPUT")
	for _, s := range list {
		st := s.Status
		if s.ExitCode != nil {
			st = fmt.Sprintf("%s(%d)", st, *s.ExitCode)
		}
		cmd := strings.TrimSpace(s.Cmd + " " + strings.Join(s.Args, " "))
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n", s.ID[:8], s.Name, st, s.PID, s.Cwd, cmd, ago(s.LastOutputAt))
	}
	return w.Flush()
}

func runLogs() error {
	argv := service.LogsArgs("")
	if argv == nil {
		return service.ErrUnsupported
	}
	c := exec.Command(argv[0], argv[1:]...)
	c.Stdout, c.Stderr, c.Stdin = os.Stdout, os.Stderr, os.Stdin
	return c.Run()
}

// runHook is invoked by Claude Code hooks: `orchestrator _hook <event>`.
// It must never block Claude, so all failures are swallowed.
func runHook(args []string) error {
	if len(args) < 1 {
		return nil
	}
	h := claude.ParseHookPayload(os.Stdin)
	io.Copy(io.Discard, os.Stdin) // never leave Claude blocked on a full pipe
	h.Event = args[0]
	sid := os.Getenv("ORCHESTRATOR_SESSION_ID")
	if sid == "" {
		return nil
	}
	var cl *admin.Client
	if sock := os.Getenv("ORCHESTRATOR_SOCKET"); sock != "" {
		cl = admin.NewClient(sock)
	} else if c, err := adminClient(""); err == nil {
		cl = c
	} else {
		return nil
	}
	_ = cl.Hook(sid, h)
	return nil
}

func ago(ms int64) string {
	if ms == 0 {
		return "-"
	}
	d := time.Since(time.UnixMilli(ms)).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
