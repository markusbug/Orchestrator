package server

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/url"
	"path/filepath"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"github.com/markusbug/Orchestrator/daemon/internal/auth"
)

// newApexTLS builds the TLS configuration for connections whose SNI is the
// relay's own domain. Only the apex ever gets a certificate; host subdomains
// are passed through to daemons untouched.
func newApexTLS(cfg *Config) (*tls.Config, *x509.Certificate, error) {
	if cfg.Dev {
		cert := cfg.DevCert
		if cert == nil {
			c, err := selfSigned(cfg.Domain)
			if err != nil {
				return nil, nil, err
			}
			cert = &c
		}
		leaf := cert.Leaf
		if leaf == nil {
			var err error
			if leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
				return nil, nil, err
			}
		}
		return &tls.Config{
			Certificates: []tls.Certificate{*cert},
			MinVersion:   tls.VersionTLS12,
			NextProtos:   []string{"http/1.1"},
		}, leaf, nil
	}
	// The cache is keyed by ACME directory so switching from staging to
	// production never reuses a staging account.
	dirHost := "letsencrypt"
	if cfg.ACMEDirectory != "" {
		if u, err := url.Parse(cfg.ACMEDirectory); err == nil && u.Host != "" {
			dirHost = u.Host
		}
	}
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(cfg.Domain),
		Cache:      autocert.DirCache(filepath.Join(cfg.CacheDir, dirHost)),
		Email:      cfg.ACMEEmail,
	}
	if cfg.ACMEDirectory != "" {
		m.Client = &acme.Client{DirectoryURL: cfg.ACMEDirectory}
	}
	tc := m.TLSConfig()
	tc.MinVersion = tls.VersionTLS12
	tc.NextProtos = []string{"http/1.1", acme.ALPNProto}
	return tc, nil, nil
}

// selfSigned makes the apex certificate for -dev: one year, also valid for
// localhost and the loopback addresses so a local daemon can be pointed at it.
func selfSigned(domain string) (tls.Certificate, error) {
	return auth.SelfSigned(auth.SelfSignedSpec{
		CommonName:   domain,
		Organization: "Orchestrator relay (dev)",
		ValidFor:     365 * 24 * time.Hour,
		DNSNames:     []string{domain, "localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	})
}
