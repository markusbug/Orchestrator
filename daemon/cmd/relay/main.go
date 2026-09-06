// Command relay runs an Orchestrator relay node. See docs/RELAY.md and
// deploy/relay/README.md.
package main

import (
	"context"
	"encoding/pem"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/markusbug/Orchestrator/daemon/internal/buildinfo"
	"github.com/markusbug/Orchestrator/daemon/internal/relay/server"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string) bool {
	v, _ := strconv.ParseBool(os.Getenv(key))
	return v
}

func main() {
	fs := flag.NewFlagSet("relay", flag.ExitOnError)
	domain := fs.String("domain", env("RELAY_DOMAIN", ""), "relay domain (apex); phones connect to <hostid>.<domain>")
	listen := fs.String("listen", env("RELAY_LISTEN", ":443"), "TCP listen address")
	acmeEmail := fs.String("acme-email", env("RELAY_ACME_EMAIL", ""), "contact email for Let's Encrypt (optional)")
	acmeDir := fs.String("acme-directory", env("RELAY_ACME_DIRECTORY", ""), "ACME directory URL (default Let's Encrypt production)")
	cacheDir := fs.String("cache-dir", env("STATE_DIRECTORY", "/var/lib/relay"), "certificate cache directory (the systemd unit passes its state directory)")
	metrics := fs.String("metrics", env("RELAY_METRICS_LISTEN", "127.0.0.1:9100"), "loopback address for /debug/vars, /debug/pprof and /healthz (empty disables)")
	dev := fs.Bool("dev", envBool("RELAY_DEV"), "self-signed apex certificate, no ACME")
	devCertOut := fs.String("dev-cert", env("RELAY_DEV_CERT", ""), "with -dev: write the apex certificate PEM here so a local daemon can trust it (relay ca_file)")
	logFormat := fs.String("log-format", env("RELAY_LOG_FORMAT", "json"), "json or text")
	maxStreams := fs.Int("max-streams-per-host", 0, "active phone streams per host (default 16)")
	connsPerMin := fs.Int("conns-per-minute-per-ip", 0, "new connections per minute per IP (default 30)")
	maxPeek := fs.Int("max-peeking-per-ip", 0, "concurrent TLS handshakes being inspected per IP (default 16)")
	verbose := fs.Bool("v", false, "debug logging")
	version := fs.Bool("version", false, "print version")
	fs.Parse(os.Args[1:])

	if *version {
		fmt.Println("relay", buildinfo.Version)
		return
	}
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	var handler slog.Handler
	if *logFormat == "text" {
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	} else {
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	}
	log := slog.New(handler)
	if *domain == "" {
		fmt.Fprintln(os.Stderr, "error: -domain (or RELAY_DOMAIN) is required")
		os.Exit(2)
	}
	if !*dev && *cacheDir == "" {
		fmt.Fprintln(os.Stderr, "error: -cache-dir is required without -dev")
		os.Exit(2)
	}

	srv, err := server.New(server.Config{
		Domain: *domain, ACMEEmail: *acmeEmail, ACMEDirectory: *acmeDir, CacheDir: *cacheDir, Dev: *dev,
		MaxStreamsPerHost: *maxStreams, ConnsPerMinutePerIP: *connsPerMin, MaxPeekingPerIP: *maxPeek,
		Version: buildinfo.Version, Log: log,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *metrics != "" {
		mln, err := net.Listen("tcp", *metrics)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: metrics listener:", err)
			os.Exit(1)
		}
		msrv := &http.Server{Handler: srv.MetricsHandler(), ReadHeaderTimeout: 5 * time.Second}
		go func() { _ = msrv.Serve(mln) }()
		defer func() {
			sctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = msrv.Shutdown(sctx)
		}()
	}

	log.Info("relay starting", "version", buildinfo.Version, "domain", *domain, "listen", ln.Addr().String(), "dev", *dev, "metrics", *metrics)
	if *dev {
		if c := srv.ApexCert(); c != nil {
			log.Info("dev apex certificate", "subject", c.Subject.CommonName, "not_after", c.NotAfter.Format(time.RFC3339))
			if *devCertOut != "" {
				pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
				if err := os.WriteFile(*devCertOut, pemBytes, 0o644); err != nil {
					log.Error("write dev cert", "path", *devCertOut, "err", err)
				} else {
					log.Info("dev certificate written; daemons trust it via [relay] ca_file", "path", *devCertOut)
				}
			}
		}
	} else {
		log.Info("acme", "directory", orDefault(*acmeDir, "letsencrypt production"), "cache", *cacheDir)
	}
	if err := srv.Serve(ctx, ln); err != nil {
		log.Error("relay stopped", "err", err)
		os.Exit(1)
	}
	log.Info("relay stopped")
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
