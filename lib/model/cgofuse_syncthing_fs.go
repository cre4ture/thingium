package model

import (
	"fmt"
	"io"
	"strings"

	"github.com/winfsp/cgofuse/fuse"
)

const (
	filename = "hello"
	contents = "hello, world\n"
)

type SyncthingFs struct {
	fuse.FileSystemBase

	stFolder SyncthingVirtualFolderAccessI
}

func (fs *SyncthingFs) Open(path string, flags int) (errc int, fh uint64) {
	switch path {
	case "/" + filename:
		return 0, 0
	default:
		return -fuse.ENOENT, ^uint64(0)
	}
}

func (fs *SyncthingFs) Getattr(path string, stat *fuse.Stat_t, fh uint64) (errc int) {
	switch path {
	case "/":
		stat.Mode = fuse.S_IFDIR | 0555
		return 0
	case "/" + filename:
		stat.Mode = fuse.S_IFREG | 0444
		stat.Size = int64(len(contents))
		return 0
	default:
		return -fuse.ENOENT
	}
}

func (fs *SyncthingFs) Read(path string, buff []byte, ofst int64, fh uint64) (n int) {
	endofst := ofst + int64(len(buff))
	if endofst > int64(len(contents)) {
		endofst = int64(len(contents))
	}
	if endofst < ofst {
		return 0
	}
	n = copy(buff, contents[ofst:endofst])
	return
}

func (fs *SyncthingFs) Readdir(path string,
	fill func(name string, stat *fuse.Stat_t, ofst int64) bool,
	ofst int64,
	fh uint64,
) (errc int) {

	// log inputs:
	path, _ = strings.CutPrefix(path, "/")
	l.Infof("SyncthingFs: path: %s, ofst: %d, fh: %d", path, ofst, fh)

	stream, eno := fs.stFolder.readDir(path)
	if eno != 0 {
		l.Warnf("Failed to read directory: %d", eno)
		return int(eno)
	}
	defer stream.Close()

	for stream.HasNext() {
		entry, eno := stream.Next()
		l.Infof("Processing directory entry: %s", entry.Name)
		if eno != 0 {
			return int(eno)
		}
		if !fill(entry.Name, nil, 0) {
			return 0
		}
	}

	return 0
}

type HostCloser struct {
	host        *fuse.FileSystemHost
	mountFuture chan error
}

func (h *HostCloser) Close() error {
	if h.host != nil {
		h.host.Unmount()
	}
	err := <-h.mountFuture
	return err
}

func NewSyncthingFsMount(mountPath string, folderId, folderLabel string, stFolder SyncthingVirtualFolderAccessI) (io.Closer, error) {
	// Create a new SyncthingFs instance
	syncthingFs := &SyncthingFs{
		FileSystemBase: fuse.FileSystemBase{},
		stFolder:       stFolder,
	}

	// Enable logging for FUSE operations
	l.Infof("Initializing SyncthingFs with folderId: %s and folderLabel: %s", folderId, folderLabel)

	// Create a new FUSE host
	host := fuse.NewFileSystemHost(syncthingFs)
	if host == nil {
		return nil, fmt.Errorf("failed to create cgo-FUSE host")
	}

	// Mount the file system
	mountFuture := make(chan error, 1)
	go func() {
		if ok := host.Mount(mountPath, nil); !ok {
			l.Warnf("failed to mount cgo-FUSE filesystem at %s", mountPath)
			mountFuture <- fmt.Errorf("failed to mount cgo-FUSE filesystem at %s", mountPath)
		} else {
			mountFuture <- nil
		}
	}()

	l.Infof("Mounted SyncthingFs at path: %s with folderId: %s and folderLabel: %s", mountPath, folderId, folderLabel)

	return &HostCloser{host: host, mountFuture: mountFuture}, nil
}
