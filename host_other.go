//go:build !linux

package main

import "errors"

// bondvpn runs on Linux only (main.go refuses to start anywhere else). This
// exists so the package still builds - and its tests still run - on the
// machine it is developed on.
func diskUsage(string) (total, free int64, err error) {
	return 0, 0, errors.New("disk usage is only available on Linux")
}
