package metadata

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCertFiles writes a self-signed cert+key pair and a PEM CA bundle.
func writeCertFiles(t *testing.T, dir string) (certFile, keyFile, caFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile = filepath.Join(dir, "tls.crt")
	keyFile = filepath.Join(dir, "tls.key")
	caFile = filepath.Join(dir, "ca.crt")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile, caFile
}

func TestDexTransportCredentialsInsecure(t *testing.T) {
	creds, err := DexTLSConfig{Insecure: true}.TransportCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if got := creds.Info().SecurityProtocol; got != "insecure" {
		t.Fatalf("SecurityProtocol = %q, want insecure", got)
	}
}

// The DCR path must not fall back to a plaintext admin channel: missing
// DEX_GRPC_TLS_* is an error, and the handler answers 500 instead.
func TestDexTransportCredentialsRequiresMaterial(t *testing.T) {
	for _, cfg := range []DexTLSConfig{
		{KeyFile: "k", CAFile: "c"},
		{CertFile: "c", CAFile: "c"},
		{CertFile: "c", KeyFile: "k"},
		{},
	} {
		if _, err := cfg.TransportCredentials(); err == nil {
			t.Fatalf("%+v: expected an error, got nil", cfg)
		}
	}
}

func TestDexTransportCredentialsReadsMaterial(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, caFile := writeCertFiles(t, dir)
	creds, err := DexTLSConfig{CertFile: certFile, KeyFile: keyFile, CAFile: caFile}.TransportCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if got := creds.Info().SecurityProtocol; got != "tls" {
		t.Fatalf("SecurityProtocol = %q, want tls", got)
	}
}

func TestDexTransportCredentialsRejectsBadFiles(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, caFile := writeCertFiles(t, dir)

	if _, err := (DexTLSConfig{CertFile: filepath.Join(dir, "no.crt"), KeyFile: keyFile, CAFile: caFile}).TransportCredentials(); err == nil {
		t.Fatal("missing cert file: expected an error")
	}
	if _, err := (DexTLSConfig{CertFile: certFile, KeyFile: keyFile, CAFile: filepath.Join(dir, "no.crt")}).TransportCredentials(); err == nil {
		t.Fatal("missing CA file: expected an error")
	}
	junk := filepath.Join(dir, "junk.crt")
	if err := os.WriteFile(junk, []byte("not a cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (DexTLSConfig{CertFile: certFile, KeyFile: keyFile, CAFile: junk}).TransportCredentials(); err == nil {
		t.Fatal("CA file without certificates: expected an error")
	}
}

// A cert-manager renewal must reach a running pod without a restart.
func TestDexReloadingCertPicksUpRenewal(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, _ := writeCertFiles(t, dir)

	rc, err := newReloadingCert(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	first, err := rc.GetClientCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(10 * time.Millisecond)
	otherDir := t.TempDir()
	newCertFile, newKeyFile, _ := writeCertFiles(t, otherDir)
	newCert, err := os.ReadFile(newCertFile)
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := os.ReadFile(newKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, newCert, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, newKey, 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(certFile, future, future); err != nil {
		t.Fatal(err)
	}

	second, err := rc.GetClientCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Certificate[0]) == string(second.Certificate[0]) {
		t.Fatal("cached certificate was not replaced after renewal")
	}
}

func TestDexTLSConfigFromEnv(t *testing.T) {
	t.Setenv("DEX_GRPC_TLS_CERT", "")
	t.Setenv("DEX_GRPC_TLS_KEY", "")
	t.Setenv("DEX_GRPC_TLS_CA", "")
	t.Setenv("DEX_GRPC_INSECURE", "")
	t.Setenv("DEX_GRPC_TLS_SERVER_NAME", "")

	cfg := DexTLSConfigFromEnv()
	if cfg.CertFile != "/etc/dex-grpc-tls/tls.crt" ||
		cfg.KeyFile != "/etc/dex-grpc-tls/tls.key" ||
		cfg.CAFile != "/etc/dex-grpc-tls/ca.crt" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.Insecure {
		t.Fatal("Insecure must be opt-in, never a default")
	}
	if cfg.ServerName != "" {
		t.Fatalf("ServerName = %q, want empty", cfg.ServerName)
	}

	t.Setenv("DEX_GRPC_TLS_CERT", "/c")
	t.Setenv("DEX_GRPC_TLS_KEY", "/k")
	t.Setenv("DEX_GRPC_TLS_CA", "/a")
	t.Setenv("DEX_GRPC_INSECURE", "true")
	t.Setenv("DEX_GRPC_TLS_SERVER_NAME", "home-os-dex.home-os.svc")
	cfg = DexTLSConfigFromEnv()
	if cfg.CertFile != "/c" || cfg.KeyFile != "/k" || cfg.CAFile != "/a" ||
		!cfg.Insecure || cfg.ServerName != "home-os-dex.home-os.svc" {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
}
