package client

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// TLSOptions selects additional trust for an HTTPS hub. CAFile and Fingerprint
// are mutually exclusive. A fingerprint supplements normal system-root checks.
type TLSOptions struct {
	CAFile      string
	Fingerprint string
}

func parseFingerprint(pin string) ([]byte, error) {
	if !strings.HasPrefix(pin, "sha256:") || len(pin) != len("sha256:")+sha256.Size*2 {
		return nil, errors.New("TLS fingerprint must be sha256: followed by 64 hex digits")
	}
	b, err := hex.DecodeString(strings.TrimPrefix(pin, "sha256:"))
	if err != nil {
		return nil, errors.New("TLS fingerprint must be sha256: followed by 64 hex digits")
	}
	return b, nil
}

// ValidateTLSOptions checks settings without reading files or making requests.
func ValidateTLSOptions(baseURL string, opts TLSOptions) error {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("hub URL must be an absolute http:// or https:// URL without credentials, query or fragment")
	}
	if opts.CAFile != "" && opts.Fingerprint != "" {
		return errors.New("tls_ca_file and tls_fingerprint are mutually exclusive")
	}
	if (opts.CAFile != "" || opts.Fingerprint != "") && u.Scheme != "https" {
		return errors.New("TLS trust settings require an https:// hub URL")
	}
	if opts.Fingerprint != "" {
		_, err = parseFingerprint(opts.Fingerprint)
		return err
	}
	return nil
}

// NewWithTLS creates the shared CLI/MCP transport. Validation errors are returned
// before any request; certificate failures never cause an HTTP retry.
func NewWithTLS(baseURL, token string, opts TLSOptions) (*Client, error) {
	if err := ValidateTLSOptions(baseURL, opts); err != nil {
		return nil, err
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if opts.CAFile != "" {
		data, err := os.ReadFile(opts.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read TLS CA file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(data) {
			return nil, errors.New("TLS CA file contains no valid certificates")
		}
		cfg.RootCAs = pool
	}
	if opts.Fingerprint != "" {
		pin, _ := parseFingerprint(opts.Fingerprint) // validated above
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			// InsecureSkipVerify remains false: Go verifies chain, SAN and validity
			// before calling this additional check (also on resumed sessions).
			if len(cs.VerifiedChains) == 0 || len(cs.PeerCertificates) == 0 {
				return errors.New("TLS fingerprint requires a verified certificate chain")
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(sum[:], pin) != 1 {
				return errors.New("TLS leaf certificate fingerprint mismatch")
			}
			return nil
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = cfg
	c := New(baseURL, token)
	c.HTTP.Transport = transport
	// Trust is scoped to the configured hub. Do not send credentials or file
	// bodies to a redirect target, or follow an HTTPS-to-HTTP downgrade.
	origin, _ := url.Parse(baseURL)
	c.HTTP.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if req.URL.Scheme != origin.Scheme || !strings.EqualFold(req.URL.Host, origin.Host) {
			return errors.New("hub redirect changes origin")
		}
		return nil
	}
	return c, nil
}
