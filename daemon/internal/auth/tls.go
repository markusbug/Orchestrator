// Package auth handles TLS identity, pairing, device authentication, and
// failure rate limiting.
package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Identity is the daemon's TLS certificate and its fingerprint.
type Identity struct {
	Cert        tls.Certificate
	Fingerprint string // "sha256:<hex>" of the DER certificate
}

// Fingerprint returns the sha256 fingerprint string for a DER certificate.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// EnsureTLS loads the certificate from certFile/keyFile, generating a
// self-signed one on first run. The certificate is persisted so phones can pin it.
func EnsureTLS(certFile, keyFile, hostname string) (Identity, error) {
	if _, err := os.Stat(certFile); err == nil {
		if _, err := os.Stat(keyFile); err == nil {
			return load(certFile, keyFile)
		}
	}
	if hostname == "" {
		hostname = "orchestrator"
	}
	cert, err := SelfSigned(SelfSignedSpec{
		CommonName:   hostname,
		Organization: "Orchestrator",
		ValidFor:     10 * 365 * 24 * time.Hour,
		DNSNames:     []string{hostname, "localhost"},
	})
	if err != nil {
		return Identity{}, err
	}
	der := cert.Certificate[0]
	keyDER, err := x509.MarshalECPrivateKey(cert.PrivateKey.(*ecdsa.PrivateKey))
	if err != nil {
		return Identity{}, err
	}
	if err := os.MkdirAll(filepath.Dir(certFile), 0o700); err != nil {
		return Identity{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		return Identity{}, err
	}
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		return Identity{}, err
	}
	return load(certFile, keyFile)
}

// SelfSignedSpec describes a self-signed server certificate.
type SelfSignedSpec struct {
	CommonName   string
	Organization string
	ValidFor     time.Duration
	DNSNames     []string
	IPAddresses  []net.IP
}

// SelfSigned generates a P-256 self-signed server certificate. The daemon
// uses it for its pinned identity and the relay for its -dev apex.
func SelfSigned(spec SelfSignedSpec) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: spec.CommonName, Organization: []string{spec.Organization}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(spec.ValidFor),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              spec.DNSNames,
		IPAddresses:           spec.IPAddresses,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

func load(certFile, keyFile string) (Identity, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return Identity{}, err
	}
	if len(cert.Certificate) == 0 {
		return Identity{}, errors.New("auth: empty certificate chain")
	}
	return Identity{Cert: cert, Fingerprint: Fingerprint(cert.Certificate[0])}, nil
}
