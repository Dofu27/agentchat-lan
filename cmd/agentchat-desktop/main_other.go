//go:build !windows

package main

import "fmt"

func main() {
	fmt.Println("agentchat-desktop is Windows-only; use the Web UI on other platforms")
}
