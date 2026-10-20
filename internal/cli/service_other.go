//go:build !windows

package cli

import "fmt"

func (a *App) serviceRun([]string) int {
	return a.fail(fmt.Errorf("service run is only implemented on Windows"))
}
