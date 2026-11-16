package metadata

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// DexTLSConfig is how the MCP reaches Dex's gRPC admin listener (5557) to
// create clients for dynamic client registration.
//
// Dex's gRPC API has no authentication of its own: the transport is the only
// thing between a pod on the network and CreateClient/CreatePassword (#697), so
// this client presents a certificate from the shared private CA and verifies
// Dex's server certificate against that same CA. Insecure exists for local
// development and has to be asked for explicitly.
type DexTLSConfig struct {
	CertFile   string
	KeyFile    string
	CAFile     string
	ServerName string
	Insecure   bool
}

// DexTLSConfigFromEnv reads DEX_GRPC_TLS_* / DEX_GRPC_INSECURE, defaulting to
// the paths the chart mounts the client certificate at.
func DexTLSConfigFromEnv() DexTLSConfig {
	return DexTLSConfig{
		CertFile:   envOr("DEX_GRPC_TLS_CERT", "/etc/dex-grpc-tls/tls.crt"),
		KeyFile:    envOr("DEX_GRPC_TLS_KEY", "/etc/dex-grpc-tls/tls.key"),
		CAFile:     envOr("DEX_GRPC_TLS_CA", "/etc/dex-grpc-tls/ca.crt"),
		ServerName: os.Getenv("DEX_GRPC_TLS_SERVER_NAME"),
		Insecure:   os.Getenv("DEX_GRPC_INSECURE") == "true",
	}
}

// TransportCredentials builds the gRPC credentials. It fails rather than fall
// back to plaintext when the material is missing.
func (c DexTLSConfig) TransportCredentials() (credentials.TransportCredentials, error) {
	if c.Insecure {
		return insecure.NewCredentials(), nil
	}
	if c.CertFile == "" || c.KeyFile == "" || c.CAFile == "" {
		return nil, fmt.Errorf("dex grpc: DEX_GRPC_TLS_CERT, DEX_GRPC_TLS_KEY and DEX_GRPC_TLS_CA are required (or set DEX_GRPC_INSECURE=true for local development)")
	}
	pem, err := os.ReadFile(c.CAFile)
	if err != nil {
		return nil, fmt.Errorf("dex grpc: read client CA %s: %w", c.CAFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("dex grpc: no certificates in %s", c.CAFile)
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

// reloadingCert re-reads the client certificate when the file changes, so a
// cert-manager renewal reaches a long-running MCP pod without a restart.
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
