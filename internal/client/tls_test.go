package client

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func tlsTestServer(t *testing.T, mode string, handler http.Handler) (*httptest.Server, string, string, *x509.CertPool) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(48 * time.Hour)}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0600); err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	switch mode {
	case "wrong-host":
		leaf.IPAddresses = nil
		leaf.DNSNames = []string{"wrong.invalid"}
	case "expired":
		leaf.NotBefore = time.Now().Add(-24 * time.Hour)
		leaf.NotAfter = time.Now().Add(-time.Hour)
	case "future":
		leaf.NotBefore = time.Now().Add(time.Hour)
		leaf.NotAfter = time.Now().Add(2 * time.Hour)
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: key}
	if handler == nil {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"status":"ok","version":"tls-test"}`)
		})
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caPEM)
	sum := sha256.Sum256(der)
	return srv, caFile, "sha256:" + hex.EncodeToString(sum[:]), roots
}

func TestTLSCertificateVerification(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-host", "expired", "future"} {
		srv, ca, pin, roots := tlsTestServer(t, mode, nil)
		for _, trust := range []string{"ca", "pin", "wrong-pin", "untrusted-pin", "system", "system-trusted"} {
			t.Run(mode+"/"+trust, func(t *testing.T) {
				opts := TLSOptions{}
				if trust == "ca" {
					opts.CAFile = ca
				}
				if strings.Contains(trust, "pin") {
					opts.Fingerprint = pin
				}
				if trust == "wrong-pin" {
					opts.Fingerprint = "sha256:" + strings.Repeat("0", 64)
				}
				c, err := NewWithTLS(srv.URL, "secret-test-token", opts)
				if err != nil {
					t.Fatal(err)
				}
				defer c.HTTP.CloseIdleConnections()
				// Substitute a private root pool for system roots in selected tests
				// only. No OS trust store changes or insecure verification.
				if trust == "pin" || trust == "wrong-pin" || trust == "system-trusted" {
					c.HTTP.Transport.(*http.Transport).TLSClientConfig.RootCAs = roots
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, err = c.Health(ctx)
				wantOK := mode == "valid" && (trust == "ca" || trust == "pin" || trust == "system-trusted")
				if (err == nil) != wantOK {
					t.Fatalf("success=%v want %v: %v", err == nil, wantOK, err)
				}
				if err != nil && strings.Contains(err.Error(), "secret-test-token") {
					t.Fatal("error leaked token")
				}
				if mode == "valid" && trust == "wrong-pin" && !strings.Contains(err.Error(), "fingerprint mismatch") {
					t.Fatalf("wrong pin failed for wrong reason: %v", err)
				}
				if mode == "valid" && (trust == "system" || trust == "untrusted-pin") && !strings.Contains(err.Error(), "unknown authority") {
					t.Fatalf("expected untrusted chain: %v", err)
				}
			})
		}
	}
}

func TestTLSOptionsFailBeforeRequests(t *testing.T) {
	badFile := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(badFile, []byte("invalid CA data"), 0600); err != nil {
		t.Fatal(err)
	}
	validPin := "sha256:" + strings.Repeat("a", 64)
	for name, tc := range map[string]struct {
		url  string
		opts TLSOptions
	}{
		"HTTP CA":    {"http://localhost", TLSOptions{CAFile: badFile}},
		"HTTP pin":   {"http://localhost", TLSOptions{Fingerprint: validPin}},
		"both":       {"https://localhost", TLSOptions{CAFile: badFile, Fingerprint: validPin}},
		"bad CA":     {"https://localhost", TLSOptions{CAFile: badFile}},
		"missing CA": {"https://localhost", TLSOptions{CAFile: badFile + ".missing"}},
		"short pin":  {"https://localhost", TLSOptions{Fingerprint: "sha256:aa"}},
		"nonhex pin": {"https://localhost", TLSOptions{Fingerprint: "sha256:" + strings.Repeat("z", 64)}},
		"bare pin":   {"https://localhost", TLSOptions{Fingerprint: strings.Repeat("a", 64)}},
		"userinfo":   {"https://user:secret@localhost", TLSOptions{}},
		"bad scheme": {"ftp://localhost", TLSOptions{}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewWithTLS(tc.url, "token", tc.opts); err == nil {
				t.Fatal("invalid settings accepted")
			}
		})
	}
}

func TestTLSRedirectCannotDowngradeOrChangeOrigin(t *testing.T) {
	var received atomic.Int32
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer dest.Close()
	for _, target := range []string{dest.URL, "https://localhost:1"} {
		srv, ca, _, _ := tlsTestServer(t, "valid", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target, http.StatusTemporaryRedirect)
		}))
		c, err := NewWithTLS(srv.URL, "secret", TLSOptions{CAFile: ca})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err = c.Me(ctx)
		cancel()
		c.HTTP.CloseIdleConnections()
		if err == nil || !strings.Contains(err.Error(), "changes origin") {
			t.Fatalf("redirect allowed: %v", err)
		}
	}
	if received.Load() != 0 {
		t.Fatal("redirect target received credentials")
	}
}
