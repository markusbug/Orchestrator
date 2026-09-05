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
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Identity{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return Identity{}, err
	}
	if hostname == "" {
		hostname = "orchestrator"
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: hostname, Organization: []string{"Orchestrator"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{hostname, "localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return Identity{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
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
