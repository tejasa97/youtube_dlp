//go:build !unix && !windows

package rpc

import (
	"os/exec"

	"github.com/ytdlp-go/ytdlp/internal/plugin"
)

type processIsolation struct{}

func configureIsolation(*exec.Cmd) error { return plugin.ErrIsolationUnavailable }
func attachIsolation(*exec.Cmd) (*processIsolation, error) {
	return nil, plugin.ErrIsolationUnavailable
}
func (*processIsolation) Terminate() error { return plugin.ErrIsolationUnavailable }
func (*processIsolation) Close() error     { return nil }
