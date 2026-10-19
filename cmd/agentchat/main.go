package main

import (
	"os"

	"github.com/Dofu27/agentchat-lan/internal/cli"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], version))
}
