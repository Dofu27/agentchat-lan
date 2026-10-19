package cli

import (
	"fmt"
	"os"
	"runtime"

	"github.com/Dofu27/agentchat-lan/internal/config"
)

type doctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

func (a *App) cmdDoctor(args []string) int {
	fs := a.newFlagSet("doctor")
	cfgPath := fs.String("config", "", "config file path")
	jsonOut := fs.Bool("json", false, "print JSON")
	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}

	path := configPathOrEmpty(*cfgPath)
	checks := make([]doctorCheck, 0, 5)

	cfg, err := config.Load(path)
	if err != nil {
		checks = append(checks, doctorCheck{Name: "config", OK: false, Detail: err.Error()})
		return a.finishDoctor(checks, *jsonOut)
	}
	checks = append(checks, doctorCheck{Name: "config", OK: true, Detail: path})
	if insecureHTTP(cfg.HubURL) {
		checks = append(checks, doctorCheck{Name: "transport", OK: false, Detail: "insecure non-loopback HTTP: tokens and traffic are unencrypted; use HTTPS"})
	}

	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err == nil {
			ok := info.Mode().Perm()&0o077 == 0
			detail := fmt.Sprintf("mode %04o", info.Mode().Perm())
			checks = append(checks, doctorCheck{Name: "config-permissions", OK: ok, Detail: detail})
		}
	}

	ctx, cancel := requestContext(15)
	defer cancel()

	c, err := a.newClient(cfg)
	if err != nil {
		checks = append(checks, doctorCheck{Name: "transport", OK: false, Detail: err.Error()})
		return a.finishDoctor(checks, *jsonOut)
	}
	defer c.HTTP.CloseIdleConnections()
	health, err := c.Health(ctx)
	if err != nil {
		checks = append(checks, doctorCheck{Name: "hub", OK: false, Detail: err.Error()})
		return a.finishDoctor(checks, *jsonOut)
	}
	checks = append(checks, doctorCheck{Name: "hub", OK: true, Detail: cfg.HubURL + " version=" + health.Version})

	me, err := c.Me(ctx)
	if err != nil {
		checks = append(checks, doctorCheck{Name: "auth", OK: false, Detail: err.Error()})
		return a.finishDoctor(checks, *jsonOut)
	}
	checks = append(checks, doctorCheck{
		Name:   "auth",
		OK:     true,
		Detail: fmt.Sprintf("%s (%s) rooms=%d", me.Agent.Name, me.Agent.ID, len(me.Rooms)),
	})
	checks = append(checks, doctorCheck{
		Name:   "limits",
		OK:     true,
		Detail: fmt.Sprintf("text=%d files=%d file=%d hops=%d", me.Limits.MaxTextBytes, me.Limits.MaxFilesPerMessage, me.Limits.MaxFileSize, me.Limits.MaxHops),
	})

	return a.finishDoctor(checks, *jsonOut)
}

func (a *App) finishDoctor(checks []doctorCheck, jsonOut bool) int {
	allOK := true
	for _, check := range checks {
		if !check.OK {
			allOK = false
		}
	}
	if jsonOut {
		code := a.printJSON(map[string]any{"ok": allOK, "checks": checks})
		if code != 0 {
			return code
		}
	} else {
		for _, check := range checks {
			status := "ok"
			if !check.OK {
				status = "FAIL"
			}
			fmt.Fprintf(a.Stdout, "[%s] %-18s %s\n", status, check.Name, check.Detail)
		}
	}
	if !allOK {
		return 1
	}
	return 0
}
