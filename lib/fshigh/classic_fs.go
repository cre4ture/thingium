package fshigh

import (
	"github.com/syncthing/syncthing/lib/fs"
	"github.com/syncthing/syncthing/lib/osutil"
)

type ClassicFilesystemHL struct {
	fs.Filesystem
}

var _ CommonFilesystemHL = (*ClassicFilesystemHL)(nil)

// Copy implements CommonFilesystemHL.
func (c *ClassicFilesystemHL) Copy(method fs.CopyRangeMethod, src string, dst string) error {
	return osutil.Copy(method, c, c, src, dst)
}

// RenameOrCopy implements CommonFilesystemHL.
func (c *ClassicFilesystemHL) RenameOrCopy(method fs.CopyRangeMethod, src string, dst string) error {
	return osutil.RenameOrCopy(method, c, c, src, dst)
}
