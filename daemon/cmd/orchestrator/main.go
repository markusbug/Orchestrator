// Command orchestrator runs the Orchestrator host daemon and its CLI.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
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
	"github.com/markusbug/Orchestrator/daemon/internal/config"
	"github.com/markusbug/Orchestrator/daemon/internal/core"
	"github.com/markusbug/Orchestrator/daemon/internal/service"
)

const usage = `Orchestrator - run Claude Code sessions on this machine, drive them from your phone.

Usage: orchestrator <command> [flags]

Commands:
  serve       run the daemon in the foreground
  install     install and start the daemon as a user service (systemd)
  uninstall   stop and remove the user service
  status      show daemon status, addresses, and fingerprint
  pair        print a QR code to pair a phone (valid 5 minutes)
  devices     list paired devices        (devices revoke <id>)
  sessions    list sessions              (sessions kill <id> | sessions remove <id>)
  logs        follow the service log
  version     print version

Flags for serve:
  --debug        serve the browser debug client at /_debug and skip auth on loopback
  --port N       listen port (default 7391 or config)
  --bind ADDR    bind address (default all interfaces)
  --dir PATH     config directory (default ~/.config/orchestrator)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
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
	case "logs":
		err = runLogs()
	case "version", "--version", "-v":
		fmt.Println("orchestrator", core.Version)
	case "_hook":
		err = runHook(args)
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

func client(dir string) (*admin.Client, error) {
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

	adm := &admin.Server{Core: c}
	if err := adm.Listen(p.AdminSocket); err != nil {
		return fmt.Errorf("admin socket: %w", err)
	}
	defer adm.Close()

	log.Info("orchestrator starting", "version", core.Version, "port", cfg.Port, "bind", cfg.Bind, "dir", p.Dir, "debug", dbg)
	log.Info("tls fingerprint", "fp", c.Identity.Fingerprint)
	for _, a := range c.Addrs() {
		log.Info("address", "ip", a.IP, "kind", a.Kind)
	}
	if dbg {
		log.Warn("debug mode: loopback connections are auto-authenticated", "url", fmt.Sprintf("https://localhost:%d/_debug/", cfg.Port))
	}
	srv := &api.Server{Core: c, Log: log}
	if err := srv.ListenAndServe(ctx); err != nil {
		return err
	}
	log.Info("shutting down")
	return nil
}

func runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	parseArgs(fs, args)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	unit, err := service.Install(service.Options{Exe: exe, Path: os.Getenv("PATH")})
	if err != nil {
		return err
	}
	fmt.Println("installed", unit)
	fmt.Println("service started; run `orchestrator status` then `orchestrator pair`")
	return nil
}

func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dir := fs.String("dir", "", "config directory")
	asJSON := fs.Bool("json", false, "print JSON")
	parseArgs(fs, args)
	cl, err := client(*dir)
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
		fmt.Printf("address:     %s (%s)\n", a.IP, a.Kind)
	}
	fmt.Printf("sessions:    %d\n", st.Sessions)
	fmt.Printf("devices:     %d\n", st.Devices)
	if st.Debug {
		fmt.Println("debug:       on (loopback auto-auth, /_debug enabled)")
	}
	return nil
}

func runPair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	dir := fs.String("dir", "", "config directory")
	parseArgs(fs, args)
	cl, err := client(*dir)
	if err != nil {
		return err
	}
	p, err := cl.Pair()
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(p)
	uri := "orchestrator://pair?d=" + base64.RawURLEncoding.EncodeToString(raw)
	fmt.Println()
	qrterminal.GenerateWithConfig(uri, qrterminal.Config{Level: qrterminal.L, Writer: os.Stdout, HalfBlocks: true, BlackChar: qrterminal.BLACK_BLACK, WhiteChar: qrterminal.WHITE_WHITE, BlackWhiteChar: qrterminal.BLACK_WHITE, WhiteBlackChar: qrterminal.WHITE_BLACK, QuietZone: 2})
	fmt.Println()
	fmt.Printf("Scan with the Orchestrator app, or enter manually:\n")
	fmt.Printf("  code:        %s   (expires %s)\n", p.Code, time.Unix(p.ExpiresAt, 0).Format(time.Kitchen))
	fmt.Printf("  port:        %d\n", p.Port)
	fmt.Printf("  fingerprint: %s\n", p.FP)
	if len(p.Addrs) == 0 {
		fmt.Println("  address:     (no network address found)")
	}
	for _, a := range p.Addrs {
		fmt.Printf("  address:     %s (%s)\n", a.IP, a.Kind)
	}
	fmt.Println()
	fmt.Println("The phone must be on the same network as this machine.")
	return nil
}

func runDevices(args []string) error {
	fs := flag.NewFlagSet("devices", flag.ExitOnError)
	dir := fs.String("dir", "", "config directory")
	rest := parseArgs(fs, args)
	cl, err := client(*dir)
	if err != nil {
		return err
	}
	if len(rest) >= 2 && rest[0] == "revoke" {
		if err := cl.RevokeDevice(rest[1]); err != nil {
			return err
		}
		fmt.Println("revoked", rest[1])
		return nil
	}
	devs, err := cl.Devices()
	if err != nil {
		return err
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
	rest := parseArgs(fs, args)
	cl, err := client(*dir)
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
	io.Copy(io.Discard, os.Stdin) // drain hook payload
	sid := os.Getenv("ORCHESTRATOR_SESSION_ID")
	if sid == "" {
		return nil
	}
	var cl *admin.Client
	if sock := os.Getenv("ORCHESTRATOR_SOCKET"); sock != "" {
		cl = admin.NewClient(sock)
	} else if c, err := client(""); err == nil {
		cl = c
	} else {
		return nil
	}
	_ = cl.Hook(sid, args[0])
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
