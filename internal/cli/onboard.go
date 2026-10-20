package cli

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type onboardOutput struct {
	Hub      string `json:"hub"`
	CAURL    string `json:"ca_url"`
	Name     string `json:"name,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Code     string `json:"code,omitempty"`
	Command  string `json:"command"`
	Guidance string `json:"guidance"`
}

func (a *App) cmdOnboard(args []string) int {
	fs := a.newFlagSet("onboard")
	hub := fs.String("hub", "https://agentchat.lan:8787", "hub URL")
	caURL := fs.String("ca-url", "https://raw.githubusercontent.com/Dofu27/agentchat-lan/main/deploy/tls/agentchat-lan-ca.crt", "CA download URL")
	name := fs.String("name", "", "agent name")
	kind := fs.String("kind", "generic", "agent kind")
	code := fs.String("code", "", "invite code")
	cfgPath := fs.String("config", "", "config file path")
	jsonOut := fs.Bool("json", false, "print JSON")

	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}
	nameArg := *name
	if nameArg == "" {
		nameArg = "<agent-name>"
	}
	kindArg := *kind
	if kindArg == "" {
		kindArg = "generic"
	}
	codeArg := *code
	if codeArg == "" {
		codeArg = "<invite-code>"
	}
	cfgArg := *cfgPath
	if cfgArg == "" {
		cfgArg = "./agent.json"
	}
	command := fmt.Sprintf(
		`agentchat join %s --name %s --kind %s --code %s --ca-url %s --config %s`,
		*hub, nameArg, kindArg, codeArg, *caURL, cfgArg,
	)
	guidance := "请先运行上面的 join 命令，它会从 CA URL 下载内部 CA 并保存到 config 同目录，然后执行 agentchat me --config <config> 验证。不要把 token 发到聊天里，也不要用 --insecure。"
	out := onboardOutput{
		Hub:      *hub,
		CAURL:    *caURL,
		Name:     *name,
		Kind:     *kind,
		Code:     *code,
		Command:  command,
		Guidance: guidance,
	}
	if *jsonOut {
		return a.printJSON(out)
	}
	fmt.Fprintln(a.Stdout, guidance)
	fmt.Fprintln(a.Stdout)
	fmt.Fprintln(a.Stdout, command)
	return 0
}

// downloadCA fetches a public CA PEM over HTTPS, verifies an optional SHA-256
// pin, validates that it contains at least one certificate, and installs it
// atomically next to the agent config.
func downloadCA(rawURL, expectedSHA, dest string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("--ca-url must be an absolute https:// URL")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		return fmt.Errorf("download CA: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download CA: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil {
		return fmt.Errorf("read CA: %w", err)
	}
	if len(data) > 1<<20 {
		return fmt.Errorf("CA file is larger than 1 MiB")
	}
	want := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(expectedSHA)), "sha256:")
	if want != "" {
		raw, err := hex.DecodeString(want)
		if err != nil || len(raw) != sha256.Size {
			return fmt.Errorf("--ca-sha256 must be 64 hex digits")
		}
		got := sha256.Sum256(data)
		if subtle.ConstantTimeCompare(got[:], raw) != 1 {
			return fmt.Errorf("CA SHA-256 mismatch")
		}
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return fmt.Errorf("CA URL did not return a valid PEM certificate")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("create CA directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".agentchat-ca-*")
	if err != nil {
		return fmt.Errorf("create CA temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write CA: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod CA: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close CA: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return fmt.Errorf("install CA: %w", err)
	}
	return nil
}
