package cli

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Certificates exist only in a temporary test directory, never in fixtures.
func hubTestCertificate(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "hub test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certFile, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	return certFile, keyFile, roots
}

type hubStartupWriter struct{ started chan string }

func (w hubStartupWriter) Write(p []byte) (int, error) {
	if strings.HasPrefix(string(p), "hub listening on ") {
		w.started <- string(p)
	}
	return len(p), nil
}

func TestHubServeTransports(t *testing.T) {
	cert, key, roots := hubTestCertificate(t)
	for _, tc := range []struct {
		name, listen string
		tls, warning bool
	}{
		{"loopback HTTP", "127.0.0.1:0", false, false},
		{"loopback HTTPS", "127.0.0.1:0", true, false},
		{"wildcard HTTP", "0.0.0.0:0", false, true},
		{"wildcard HTTPS", "0.0.0.0:0", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			var stderr bytes.Buffer
			started := make(chan string, 1)
			app := &App{Version: "test", Stdout: hubStartupWriter{started}, Stderr: &stderr}
			args := []string{"--data", t.TempDir(), "--listen", tc.listen}
			if tc.tls {
				args = append(args, "--tls-cert", cert, "--tls-key", key)
			}
			done := make(chan int, 1)
			go func() { done <- app.hubServeContext(ctx, args) }()
			t.Cleanup(func() {
				cancel()
				select {
				case code := <-done:
					if code != 0 {
						t.Errorf("serve exit=%d: %s", code, stderr.String())
					}
					if strings.Contains(stderr.String(), "WARNING: non-loopback HTTP") != tc.warning {
						t.Errorf("unexpected warning: %s", stderr.String())
					}
				case <-time.After(15 * time.Second):
					t.Error("hub did not stop")
				}
			})
			var line string
			select {
			case line = <-started:
			case <-time.After(10 * time.Second):
				t.Fatal("hub did not start")
			}
			u, err := url.Parse(strings.Fields(line)[3])
			if err != nil {
				t.Fatal(err)
			}
			wantScheme := "http"
			if tc.tls {
				wantScheme = "https"
			}
			if u.Scheme != wantScheme {
				t.Fatalf("startup URL=%s", u)
			}
			u.Host = net.JoinHostPort("127.0.0.1", u.Port())
			transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
			for _, endpoint := range []struct {
				path   string
				status int
			}{{"/healthz", 200}, {"/readyz", 200}, {"/v1/me", 401}} {
				resp, err := client.Get(u.String() + endpoint.path)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != endpoint.status {
					t.Errorf("%s status=%d", endpoint.path, resp.StatusCode)
				}
			}
			if tc.tls {
				conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", u.Host, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12})
				if err != nil {
					t.Fatal(err)
				}
				conn.Close()
				old, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", u.Host, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11})
				if err == nil {
					old.Close()
					t.Error("accepted TLS older than 1.2")
				}
				u.Scheme = "http"
				resp, err := client.Get(u.String() + "/healthz")
				if err == nil {
					resp.Body.Close()
					if resp.StatusCode == 200 {
						t.Error("HTTPS listener served plaintext HTTP health")
					}
				}
			}
		})
	}
}

func TestHubTLSStartupFailsClosed(t *testing.T) {
	cert, key, _ := hubTestCertificate(t)
	_, otherKey, _ := hubTestCertificate(t)
	bad := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(bad, []byte("not a certificate or key"), 0600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing.pem")
	for name, flags := range map[string][]string{
		"cert only": {"--tls-cert", cert}, "key only": {"--tls-key", key},
		"empty cert only": {"--tls-cert="}, "empty pair": {"--tls-cert=", "--tls-key="},
		"empty key":      {"--tls-cert", cert, "--tls-key="},
		"bad cert":       {"--tls-cert", bad, "--tls-key", key},
		"bad key":        {"--tls-cert", cert, "--tls-key", bad},
		"missing cert":   {"--tls-cert", missing, "--tls-key", key},
		"missing key":    {"--tls-cert", cert, "--tls-key", missing},
		"mismatched key": {"--tls-cert", cert, "--tls-key", otherKey},
	} {
		t.Run(name, func(t *testing.T) {
			// An occupied address proves TLS validation happens before binding.
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := ln.Addr().String()
			defer ln.Close()
			data := filepath.Join(t.TempDir(), "unopened")
			var stdout, stderr bytes.Buffer
			app := &App{Stdout: &stdout, Stderr: &stderr}
			args := append([]string{"hub", "serve", "--data", data, "--listen", addr}, flags...)
			if code := app.Run(args); code == 0 {
				t.Fatal("invalid TLS accepted")
			}
			if !strings.Contains(stderr.String(), "tls-") && !strings.Contains(stderr.String(), "TLS certificate/key") {
				t.Fatalf("unexpected error: %s", stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("unexpected startup output: %s", stdout.String())
			}
			if _, err := os.Stat(data); !os.IsNotExist(err) {
				t.Fatalf("opened database before TLS validation: %v", err)
			}
			ln.Close()
			rebound, err := net.Listen("tcp", addr)
			if err != nil {
				t.Fatalf("listener left open: %v", err)
			}
			rebound.Close()
		})
	}
}

func TestHubDefaultListen(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := &App{Stdout: &stdout, Stderr: &stderr}
	app.Run([]string{"hub", "serve", "--help"})
	if !strings.Contains(stdout.String()+stderr.String(), `default "127.0.0.1:8787"`) {
		t.Fatal("default listener changed")
	}
}
