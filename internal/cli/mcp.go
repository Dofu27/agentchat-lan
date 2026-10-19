package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Dofu27/agentchat-lan/internal/client"
	"github.com/Dofu27/agentchat-lan/internal/config"
	"github.com/Dofu27/agentchat-lan/internal/mcpserver"
)

func (a *App) cmdMCP(args []string) int {
	fs := a.newFlagSet("mcp")
	cfgPath := fs.String("config", "", "agent config file path")
	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}
	if fs.NArg() != 0 {
		return a.fail(fmt.Errorf("mcp takes no positional arguments"))
	}

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return a.fail(err)
	}
	path := configPathOrEmpty(*cfgPath)
	downloadDir, err := config.DownloadDirectory(path, cfg)
	if err != nil {
		return a.fail(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// stdout is reserved for MCP JSON-RPC; diagnostics go to stderr.
	fmt.Fprintf(a.Stderr, "agentchat mcp: hub=%s agent=%s download_dir=%s\n", cfg.HubURL, cfg.AgentName, downloadDir)
	if err := mcpserver.Run(ctx, client.New(cfg.HubURL, cfg.Token), downloadDir); err != nil {
		return a.fail(err)
	}
	return 0
}
