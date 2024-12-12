package fshigh

import (
	"github.com/syncthing/syncthing/lib/fs"
)

type CommonFilesystemHL interface {
	fs.CommonFilesystemLL
	Copy(method fs.CopyRangeMethod, from, to string) error
	RenameOrCopy(method fs.CopyRangeMethod, from, to string) error
}
