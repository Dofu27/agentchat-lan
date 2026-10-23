//go:build windows

package main

// expectedCLISHA256 is injected at build time from the exact agentchat.exe
// payload placed next to AgentChatConsole.exe. An empty value disables
// machine-wide service installation rather than trusting an unverified copy.
var expectedCLISHA256 string
