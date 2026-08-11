//go:build !windows

package fragment

import (
	"os"
	"path/filepath"
)

type noClobberUnixOps struct {
	syncSource    func(string) error
	link          func(string, string) error
	removeSource  func(string) error
	syncDirectory func(string) error
}

var productionNoClobberUnixOps = noClobberUnixOps{
	syncSource: func(path string) error {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return err
		}
		return file.Close()
	},
	link:          os.Link,
	removeSource:  os.Remove,
	syncDirectory: syncCheckpointDirectory,
}

func publishNoClobber(source, destination string) error {
	return publishNoClobberUnix(source, destination, productionNoClobberUnixOps)
}

func publishNoClobberUnix(source, destination string, ops noClobberUnixOps) error {
	if err := ops.syncSource(source); err != nil {
		return noClobberFailure("sync source", err, false, false)
	}
	if err := ops.link(source, destination); err != nil {
		return noClobberFailure("install destination link", err, false, false)
	}
	if err := ops.syncDirectory(filepath.Dir(destination)); err != nil {
		return noClobberFailure("sync destination directory", err, true, false)
	}
	if err := ops.removeSource(source); err != nil {
		return noClobberFailure("remove installed source name", err, true, false)
	}
	if err := ops.syncDirectory(filepath.Dir(source)); err != nil {
		return noClobberFailure("sync source directory", err, true, false)
	}
	return nil
}
