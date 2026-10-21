package main

import (
	"regexp"
	"strings"
)

const outputTruncatedMarker = "[output truncated]"

var outputRedactions = []*regexp.Regexp{
	regexp.MustCompile(`(?is)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
	// A budget cutoff or process failure can omit END. Complete blocks were
	// already removed above, so any remaining BEGIN consumes the entire tail.
	regexp.MustCompile(`(?is)-----BEGIN [A-Z ]*PRIVATE KEY-----.*$`),
	regexp.MustCompile(`(?i)["']?(?:authorization|proxy-authorization|authentication)["']?\s*[:=]\s*(?:"(?:\\.|[^"\\\r\n])*"|'(?:\\.|[^'\\\r\n])*'|[^\r\n,}]+)`),
	regexp.MustCompile(`(?i)["']?(?:agentchat_token|access_token|refresh_token|token|secret|password|api[_-]?key|key)["']?\s*[:=]\s*(?:"(?:\\.|[^"\\\r\n])*"|'(?:\\.|[^'\\\r\n])*'|[^&\s,;"'}]+)`),
	regexp.MustCompile(`(?i)\bBearer\s+[^\s"',;}&]+`),
	regexp.MustCompile(`agt_[A-Za-z0-9_-]+|adm_[A-Za-z0-9_-]+|github_pat_[A-Za-z0-9_]+`),
	regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^/\s@]+@`),
}

func redactOutput(s string) string {
	// Keep capture metadata outside the payload matched by EOF redactors.
	truncated := strings.HasSuffix(s, outputTruncatedMarker)
	if truncated {
		s = strings.TrimSuffix(strings.TrimSuffix(s, outputTruncatedMarker), "\n")
	}
	for _, pattern := range outputRedactions {
		s = pattern.ReplaceAllString(s, "<redacted>")
	}
	if truncated {
		if s == "" {
			return outputTruncatedMarker
		}
		return s + "\n" + outputTruncatedMarker
	}
	return s
}
