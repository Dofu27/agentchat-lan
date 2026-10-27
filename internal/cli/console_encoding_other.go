//go:build !windows

package cli

func decodeConsoleOutput(b []byte) string {
	return string(b)
}
