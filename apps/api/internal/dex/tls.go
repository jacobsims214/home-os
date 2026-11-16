package dex

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// TLSConfig is how the API reaches Dex's gRPC admin listener (port 5557).
//
// Dex's gRPC API is unauthenticated at the application layer: anyone who can
// complete the TCP connection can call CreatePassword (set the password of any
// user, including the instance admin) and CreateClient (mint an OAuth client).
// The only thing that keeps that surface closed is the transport, so this
// client presents a client certificate and verifies Dex's server certificate
// against a private CA. There is deliberately no silent plaintext fallback:
// Insecure is a local-development switch that has to be turned on explicitly.
//
// The chart wires these from the cert-manager-issued client certificate that
// the API Deployment mounts at /etc/dex-grpc-tls (#697).
type TLSConfig struct {
	// CertFile and KeyFile are the PEM client certificate and key.
	CertFile string
	KeyFile  string
	// CAFile is the PEM bundle Dex's server certificate is verified against.
	// It is the same private CA that Dex is configured to trust for client
	// certificates (grpc.tlsClientCA), so one file serves both directions.
	CAFile string
	// ServerName overrides the name verified against the server certificate.
	// Empty means the host from the dial address ("home-os-dex"), which the
	// issued certificate carries as a DNS SAN.
	ServerName string
	// Insecure disables TLS entirely. Local development only (docker compose
	// runs Dex without TLS); every deployment must leave it off.
	Insecure bool
}

// TLSConfigFromEnv reads the DEX_GRPC_TLS_* / DEX_GRPC_INSECURE environment
// variables. The defaults are the paths the chart mounts the client
// certificate at, so a Deployment that mounts the Secret does not also have to
// spell them out.
func TLSConfigFromEnv() TLSConfig {
	return TLSConfig{
		CertFile:   getenvDefault("DEX_GRPC_TLS_CERT", "/etc/dex-grpc-tls/tls.crt"),
		KeyFile:    getenvDefault("DEX_GRPC_TLS_KEY", "/etc/dex-grpc-tls/tls.key"),
		CAFile:     getenvDefault("DEX_GRPC_TLS_CA", "/etc/dex-grpc-tls/ca.crt"),
		ServerName: os.Getenv("DEX_GRPC_TLS_SERVER_NAME"),
		Insecure:   os.Getenv("DEX_GRPC_INSECURE") == "true",
	}
}

// TransportCredentials builds the gRPC credentials for this configuration.
// It fails rather than degrade to plaintext when the material is missing or
// unreadable.
func (c TLSConfig) TransportCredentials() (credentials.TransportCredentials, error) {
	if c.Insecure {
		return insecure.NewCredentials(), nil
	}
	if c.CertFile == "" || c.KeyFile == "" || c.CAFile == "" {
		return nil, errors.New("dex grpc: DEX_GRPC_TLS_CERT, DEX_GRPC_TLS_KEY and DEX_GRPC_TLS_CA are required (or set DEX_GRPC_INSECURE=true for local development)")
	}

	pool, err := loadCAPool(c.CAFile)
	if err != nil {
		return nil, err
	}
	cert, err := newReloadingCert(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, err
	}

	return credentials.NewTLS(&tls.Config{
		MinVersion:           tls.VersionTLS12,
		RootCAs:              pool,
		ServerName:           c.ServerName,
		GetClientCertificate: cert.GetClientCertificate,
	}), nil
}

func loadCAPool(caFile string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("dex grpc: read client CA %s: %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("dex grpc: no certificates in %s", caFile)
	}
	return pool, nil
}

// reloadingCert serves the client certificate from disk, re-reading it when
// the file changes. cert-manager renews the Secret behind the projected volume
// (and writes the new files in place), so a long-running API pod picks up a
// renewed certificate without a restart — otherwise the pod would keep
// presenting the old certificate until it expired and every Dex call would
// fail the handshake.
type reloadingCert struct {
	certFile string
	keyFile  string

	mu      sync.Mutex
	cert    *tls.Certificate
	modTime time.Time
}

func newReloadingCert(certFile, keyFile string) (*reloadingCert, error) {
	r := &reloadingCert{certFile: certFile, keyFile: keyFile}
	if _, err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

// GetClientCertificate implements tls.Config.GetClientCertificate.
func (r *reloadingCert) GetClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, err := os.Stat(r.certFile)
	if err != nil {
		return nil, fmt.Errorf("dex grpc: stat client cert %s: %w", r.certFile, err)
	}
	if r.cert == nil || !info.ModTime().Equal(r.modTime) {
		if _, err := r.load(); err != nil {
			return nil, err
		}
	}
	return r.cert, nil
}

// load reads the keypair and records the certificate's mtime. Caller holds mu
// (or is the constructor).
func (r *reloadingCert) load() (*tls.Certificate, error) {
	info, err := os.Stat(r.certFile)
	if err != nil {
		return nil, fmt.Errorf("dex grpc: stat client cert %s: %w", r.certFile, err)
	}
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return nil, fmt.Errorf("dex grpc: load client keypair: %w", err)
	}
	r.cert = &cert
	r.modTime = info.ModTime()
	return r.cert, nil
}

func getenvDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
