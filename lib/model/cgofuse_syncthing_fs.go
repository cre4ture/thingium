package model

import (
	"fmt"
	"io"
	"strings"

	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/winfsp/cgofuse/fuse"
)

const (
	filename = "hello"
	contents = "hello, world\n"
)

type FreeFileHandleProvider struct {
	// nextFreeByCnt
	nextFreeByCnt uint64
	// returned, free handles
	freeHandles map[uint64]struct{}
}

func NewFreeFileHandleProvider() *FreeFileHandleProvider {
	return &FreeFileHandleProvider{
		nextFreeByCnt: 2, // start from 2, as 1 is reserved for the root directory
		freeHandles:   make(map[uint64]struct{}),
	}
}
func (p *FreeFileHandleProvider) GetFreeFileHandle() uint64 {
	if len(p.freeHandles) > 0 {
		// take any handle from the map
		for handle := range p.freeHandles {
			delete(p.freeHandles, handle)
			return handle
		}
	}
	// no free handles, return a new one
	result := p.nextFreeByCnt
	p.nextFreeByCnt++
	return result
}
func (p *FreeFileHandleProvider) ReturnFreeHandle(handle uint64) {
	// add the handle to the free handles map
	p.freeHandles[handle] = struct{}{}
}

type SyncthingFs struct {
	fuse.FileSystemBase
	stFolder        SyncthingVirtualFolderAccessI
	freeFileHandles *FreeFileHandleProvider
	usedFileHandles map[uint64]*protocol.FileInfo
}

func NewSyncthingFsMount(
	mountPath string, folderId, folderLabel string, stFolder SyncthingVirtualFolderAccessI,
) (io.Closer, error) {
	// Create a new SyncthingFs instance
	syncthingFs := &SyncthingFs{
		FileSystemBase:  fuse.FileSystemBase{},
		stFolder:        stFolder,
		freeFileHandles: NewFreeFileHandleProvider(),
		usedFileHandles: make(map[uint64]*protocol.FileInfo),
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

func (fs *SyncthingFs) Open(path string, flags int) (errc int, fh uint64) {
	// log inputs:
	path, _ = strings.CutPrefix(path, "/")
	l.Infof("SyncthingFs - Open: path: %s, flags: %d", path, flags)

	// Check if the path exists in the virtual folder
	entry, eno := fs.stFolder.lookupFile(path)
	if eno != 0 {
		l.Warnf("Failed to open file: %s, error: %d", path, eno)
		return int(eno), 0
	}

	// Log the entry attributes
	l.Infof("SyncthingFs - Open: entry: name: %s, size: %d, modTime: %v, isDir: %t",
		entry.Name, entry.Size, entry.ModTime(), entry.IsDirectory())

	myHandle := fs.freeFileHandles.GetFreeFileHandle()
	l.Infof("SyncthingFs - Open: freeHandle: %d", myHandle)
	fs.usedFileHandles[myHandle] = entry

	return 0, myHandle
}

func (fs *SyncthingFs) Getattr(path string, stat *fuse.Stat_t, fh uint64) (errc int) {

	// log inputs:
	path, _ = strings.CutPrefix(path, "/")
	l.Infof("SyncthingFs - Getattr: path: %s, fh: %d", path, fh)

	if path == "" {
		// special case for root directory
		stat.Mode = fuse.S_IFDIR | 0555
		return 0
	}

	// Check if the path exists in the virtual folder
	entry, eno := fs.stFolder.lookupFile(path)
	if eno != 0 {
		l.Warnf("Failed to get attributes for path: %s, error: %d", path, eno)
		return int(eno)
	}

	// Log the entry attributes
	l.Infof("SyncthingFs - Getattr: entry: name: %s, size: %d, modTime: %v, isDir: %t, permissions: %o",
		entry.Name, entry.Size, entry.ModTime(), entry.IsDirectory(), entry.Permissions)

	// Populate the stat structure with the attributes
	permissions := entry.Permissions
	if !entry.HasPermissionBits() {
		if entry.IsDirectory() {
			permissions = 0777 // rwx for owner, rwx for group and others
		} else {
			permissions = 0666 // rw for owner, rw for group and others
		}
	}
	stat.Mode = 0
	if entry.IsDirectory() {
		stat.Mode = fuse.S_IFDIR | permissions
	} else {
		stat.Mode = fuse.S_IFREG | permissions
	}
	blkSize := int64(entry.BlockSize())
	l.Infof("SyncthingFs - Getattr: blkSize: %d", blkSize)
	stat.Size = entry.Size
	stat.Atim = fuse.NewTimespec(entry.ModTime())
	stat.Mtim = fuse.NewTimespec(entry.ModTime())
	stat.Ctim = fuse.NewTimespec(entry.ModTime())
	stat.Nlink = 1
	l.Infof("SyncthingFs - Getattr: permissions: %o, isDir: %t", permissions, entry.IsDirectory())
	unixData := entry.PlatformData().Unix
	if unixData != nil {
		stat.Uid = uint32(unixData.UID)
		stat.Gid = uint32(unixData.GID)
	} else {
		stat.Uid = 0
		stat.Gid = 0
	}
	stat.Rdev = 0
	stat.Blksize = blkSize
	stat.Blocks = (entry.Size + blkSize - 1) / blkSize
	l.Infof("SyncthingFs - Getattr: blkSize: %d, blocks: %d", blkSize, stat.Blocks)
	stat.Birthtim = fuse.NewTimespec(entry.ModTime())
	stat.Flags = 0

	// log the attributes
	l.Infof("SyncthingFs - Getattr: path: %s, mode: %o, size: %d, atim: %v, mtim: %v, ctim: %v",
		path, stat.Mode, stat.Size, stat.Atim, stat.Mtim, stat.Ctim)
	l.Infof("SyncthingFs - Getattr: uid: %d, gid: %d, nlink: %d, blksize: %d, blocks: %d",
		stat.Uid, stat.Gid, stat.Nlink, stat.Blksize, stat.Blocks)
	l.Infof("SyncthingFs - Getattr: birthtim: %v, flags: %d",
		stat.Birthtim, stat.Flags)

	return 0
}

func (fs *SyncthingFs) Read(path string, buff []byte, ofst int64, fh uint64) (n int) {

	l.Infof("SyncthingFs - Read: path: %s, ofst: %d, fh: %d", path, ofst, fh)

	// get info from the file handle
	entry, ok := fs.usedFileHandles[fh]
	if !ok {
		l.Warnf("Failed to read file: %s (%s), error: file handle not found", path, entry.Name)
		return 0
	}

	l.Infof("SyncthingFs - Read: path: %s, entry: name: %s, size: %d, modTime: %v, isDir: %t",
		path, entry.Name, entry.Size, entry.ModTime(), entry.IsDirectory())

	endofst := ofst + int64(len(buff))
	if endofst > int64(len(contents)) {
		endofst = int64(len(contents))
	}
	if endofst < ofst {
		return 0
	}

	rres, errno := fs.stFolder.readFile(entry.Name, buff, ofst)
	if errno != 0 || rres == nil {
		l.Warnf("Failed to read file: %s, error: %d", path, errno)
		return 0
	}
	defer rres.Done()

	newBuff, status := rres.Bytes(buff)
	if status != 0 {
		l.Warnf("Failed to read file: %s, error: %d", path, status)
		return 0
	}

	// Copy the data to the buffer
	n = copy(buff, newBuff)
	if n > 0 {
		l.Infof("SyncthingFs - Read: read %d bytes from file: %s", n, path)
	} else {
		l.Warnf("SyncthingFs - Read: no data read from file: %s", path)
	}

	return n
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
		//l.Debugf("Processing directory entry: %s", entry.Name)
		if eno != 0 {
			return int(eno)
		}
		mode := fuse.S_IFREG
		if entry.Type == protocol.FileInfoTypeDirectory {
			mode = fuse.S_IFDIR
		}
		blkSize := int64(128 * 1024) // syncthing block size
		stat := &fuse.Stat_t{
			Dev:      0, // ignored
			Ino:      0, // ignored
			Mode:     uint32(mode),
			Nlink:    1,
			Uid:      0,
			Gid:      0,
			Rdev:     0, // ignored
			Size:     entry.Size,
			Atim:     fuse.NewTimespec(entry.ModTime),
			Mtim:     fuse.NewTimespec(entry.ModTime),
			Ctim:     fuse.NewTimespec(entry.ModTime),
			Blksize:  blkSize,
			Blocks:   (entry.Size + int64(blkSize) - 1) / int64(blkSize),
			Birthtim: fuse.NewTimespec(entry.ModTime),
			Flags:    0,
		}
		if !fill(entry.Name, stat, 0) {
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
