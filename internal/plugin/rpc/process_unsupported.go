//go:build !unix && !windows

package rpc

import (
	"os/exec"

	"github.com/tejasa97/youtube_dlp/internal/plugin"
	"github.com/tejasa97/youtube_dlp/internal/sandbox"
)

type processIsolation struct{}

func configureIsolation(*exec.Cmd) error { return plugin.ErrIsolationUnavailable }
func attachIsolation(*exec.Cmd, sandbox.Limits) (*processIsolation, error) {
	return nil, plugin.ErrIsolationUnavailable
}
func (*processIsolation) Terminate() error { return plugin.ErrIsolationUnavailable }
func (*processIsolation) Close() error     { return nil }
