package main

import "regexp"

var outputRedactions = []*regexp.Regexp{
	regexp.MustCompile(`(?is)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?is)-----BEGIN [A-Z ]*PRIVATE KEY-----.*`),
	regexp.MustCompile(`(?i)["']?(?:authorization|proxy-authorization|authentication)["']?\s*[:=]\s*(?:"(?:\\.|[^"\\\r\n])*"|'(?:\\.|[^'\\\r\n])*'|[^\r\n,}]+)`),
	regexp.MustCompile(`(?i)["']?(?:authorization|proxy-authorization|authentication)["']?\s*[:=]\s*"(?:\\.|[^"\\\r\n])*$`),
	regexp.MustCompile(`(?i)["']?(?:agentchat_token|access_token|refresh_token|token|secret|password|api[_-]?key|key)["']?\s*[:=]\s*(?:"(?:\\.|[^"\\\r\n])*"|'(?:\\.|[^'\\\r\n])*'|[^&\s,;"'}]+)`),
	regexp.MustCompile(`(?i)["']?(?:agentchat_token|access_token|refresh_token|token|secret|password|api[_-]?key|key)["']?\s*[:=]\s*"(?:\\.|[^"\\\r\n])*$`),
	regexp.MustCompile(`(?i)["']?(?:authorization|proxy-authorization|authentication)["']?\s*[:=]\s*"[^\r\n]*$`),
	regexp.MustCompile(`(?i)["']?(?:agentchat_token|access_token|refresh_token|token|secret|password|api[_-]?key|key)["']?\s*[:=]\s*"[^\r\n]*$`),
	regexp.MustCompile(`(?i)\bBearer\s+[^\s"',;}&]+`),
	regexp.MustCompile(`agt_[A-Za-z0-9_-]+|adm_[A-Za-z0-9_-]+|github_pat_[A-Za-z0-9_]+`),
	regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^/\s@]+@`),
}

func redactOutput(s string) string {
	for _, pattern := range outputRedactions {
		s = pattern.ReplaceAllString(s, "<redacted>")
	}
	return s
}
