// Copyright (C) 2024 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build !windows
// +build !windows

package model

import (
	"context"
	"sync"
	"time"

	//	"time"

	"syscall"

	ffs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/syncthing/syncthing/lib/logger"
	"github.com/syncthing/syncthing/lib/protocol"
)

func NewVirtualFile(rel_name string, ino uint64, sVF SyncthingVirtualFolderAccessI) ffs.FileHandle {
	return &virtualFuseFile{sVF: sVF, rel_name: rel_name, ino: ino}
}

type virtualFuseFile struct {
	sVF      SyncthingVirtualFolderAccessI
	ino      uint64
	rel_name string // relative filepath

	fileInfo_mu sync.Mutex
	fileInfo    *protocol.FileInfo
}

func (f *virtualFuseFile) getFileInfo() (*protocol.FileInfo, syscall.Errno) {
	f.fileInfo_mu.Lock()
	defer f.fileInfo_mu.Unlock()

	if f.fileInfo == nil {
		var eno syscall.Errno
		f.fileInfo, eno = f.sVF.lookupFile(f.rel_name)
		if eno != 0 {
			return nil, eno
		}
	}
	return f.fileInfo, 0
}

var _ = (ffs.FileHandle)((*virtualFuseFile)(nil))
var _ = (ffs.FileReleaser)((*virtualFuseFile)(nil))
var _ = (ffs.FileReader)((*virtualFuseFile)(nil))
var _ = (ffs.FileWriter)((*virtualFuseFile)(nil))
var _ = (ffs.FileGetlker)((*virtualFuseFile)(nil))
var _ = (ffs.FileSetlker)((*virtualFuseFile)(nil))
var _ = (ffs.FileSetlkwer)((*virtualFuseFile)(nil))
var _ = (ffs.FileLseeker)((*virtualFuseFile)(nil))
var _ = (ffs.FileFlusher)((*virtualFuseFile)(nil))
var _ = (ffs.FileFsyncer)((*virtualFuseFile)(nil))
var _ = (ffs.FileAllocater)((*virtualFuseFile)(nil))

func (f *virtualFuseFile) Read(ctx context.Context, buf []byte, off int64) (res fuse.ReadResult, errno syscall.Errno) {
	logger.DefaultLogger.Infof("virtualFile Read(len, off): %v, %v", len(buf), off)
	return f.sVF.readFile(f.rel_name, buf, off)
}

func (f *virtualFuseFile) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	if off < 0 {
		return 0, syscall.EINVAL
	}

	logger.DefaultLogger.Infof("virtualFile Write(len, off): %v, %v", len(data), off)
	eno := f.sVF.writeFile(ctx, f.rel_name, uint64(off), data)
	if eno != 0 {
		return 0, eno
	}

	return uint32(len(data)), 0
}

func (f *virtualFuseFile) Release(ctx context.Context) syscall.Errno {
	return ffs.OK
}

func (f *virtualFuseFile) Flush(ctx context.Context) syscall.Errno {
	logger.DefaultLogger.Infof("virtualFile Flush(file): %s", f.rel_name)
	return ffs.OK
}

func (f *virtualFuseFile) Fsync(ctx context.Context, flags uint32) (errno syscall.Errno) {
	logger.DefaultLogger.Infof("virtualFile Fsync(file, flags): %s, %v", f.rel_name, flags)
	return ffs.OK
}

const (
	_OFD_GETLK  = 36
	_OFD_SETLK  = 37
	_OFD_SETLKW = 38
)

func (f *virtualFuseFile) Getlk(ctx context.Context, owner uint64, lk *fuse.FileLock, flags uint32, out *fuse.FileLock) (errno syscall.Errno) {
	//f.mu.Lock()
	//defer f.mu.Unlock()
	//flk := syscall.Flock_t{}
	//lk.ToFlockT(&flk)
	//errno = ffs.ToErrno(syscall.FcntlFlock(uintptr(f.fd), _OFD_GETLK, &flk))
	//out.FromFlockT(&flk)
	//return
	return syscall.ENOSYS
}

func (f *virtualFuseFile) Setlk(ctx context.Context, owner uint64, lk *fuse.FileLock, flags uint32) (errno syscall.Errno) {
	//return f.setLock(ctx, owner, lk, flags, false)
	return syscall.ENOSYS
}

func (f *virtualFuseFile) Setlkw(ctx context.Context, owner uint64, lk *fuse.FileLock, flags uint32) (errno syscall.Errno) {
	//return f.setLock(ctx, owner, lk, flags, true)
	return syscall.ENOSYS
}

func (f *virtualFuseFile) setLock(ctx context.Context, owner uint64, lk *fuse.FileLock, flags uint32, blocking bool) (errno syscall.Errno) {
	//if (flags & fuse.FUSE_LK_FLOCK) != 0 {
	//	var op int
	//	switch lk.Typ {
	//	case syscall.F_RDLCK:
	//		op = syscall.LOCK_SH
	//	case syscall.F_WRLCK:
	//		op = syscall.LOCK_EX
	//	case syscall.F_UNLCK:
	//		op = syscall.LOCK_UN
	//	default:
	//		return syscall.EINVAL
	//	}
	//	if !blocking {
	//		op |= syscall.LOCK_NB
	//	}
	//	return ffs.ToErrno(syscall.Flock(f.fd, op))
	//} else {
	//	flk := syscall.Flock_t{}
	//	lk.ToFlockT(&flk)
	//	var op int
	//	if blocking {
	//		op = _OFD_SETLKW
	//	} else {
	//		op = _OFD_SETLK
	//	}
	//	return ffs.ToErrno(syscall.FcntlFlock(uintptr(f.fd), op, &flk))
	//}
	return syscall.ENOSYS
}

var _ = (ffs.FileSetattrer)((*virtualFuseFile)(nil))

func (f *virtualFuseFile) Setattr(ctx context.Context, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	logger.DefaultLogger.Infof("virtualFuseFile Setattr(in,out): %+v, %+v", in, out)
	f.Getattr(ctx, out)
	return 0
}

func (f *virtualFuseFile) fchmod(mode uint32) syscall.Errno {
	// TODO
	return syscall.ENOSYS
}

func (f *virtualFuseFile) fchown(uid, gid int) syscall.Errno {
	// TODO
	return syscall.ENOSYS
}

func (f *virtualFuseFile) ftruncate(sz uint64) syscall.Errno {
	// TODO
	return syscall.ENOSYS
}

func (f *virtualFuseFile) setAttr(ctx context.Context, in *fuse.SetAttrIn) syscall.Errno {
	// TODO
	//willBeChangedFd(f.fd)
	//var errno syscall.Errno
	//if mode, ok := in.GetMode(); ok {
	//	if errno := f.fchmod(mode); errno != 0 {
	//		return errno
	//	}
	//}
	//
	//uid32, uOk := in.GetUID()
	//gid32, gOk := in.GetGID()
	//if uOk || gOk {
	//	uid := -1
	//	gid := -1
	//
	//	if uOk {
	//		uid = int(uid32)
	//	}
	//	if gOk {
	//		gid = int(gid32)
	//	}
	//	if errno := f.fchown(uid, gid); errno != 0 {
	//		return errno
	//	}
	//}
	//
	//mtime, mok := in.GetMTime()
	//atime, aok := in.GetATime()
	//
	//if mok || aok {
	//	ap := &atime
	//	mp := &mtime
	//	if !aok {
	//		ap = nil
	//	}
	//	if !mok {
	//		mp = nil
	//	}
	//	errno = f.utimens(ap, mp)
	//	if errno != 0 {
	//		return errno
	//	}
	//}
	//
	//if sz, ok := in.GetSize(); ok {
	//	if errno := f.ftruncate(sz); errno != 0 {
	//		return errno
	//	}
	//}
	//
	//return ffs.OK
	return syscall.ENOSYS
}

var _ = (ffs.FileGetattrer)((*virtualFuseFile)(nil))

func FileInfoToFuseAttrOut(fi *protocol.FileInfo, ino uint64, a *fuse.AttrOut) syscall.Errno {

	a.SetTimeout(time.Second)
	a.Blksize = uint32(fi.BlockSize())
	a.Blocks = uint64(fi.Size) / 512
	unix := fi.PlatformData().Unix
	if unix != nil {
		a.Gid = uint32(unix.GID)
		a.Uid = uint32(unix.UID)
	}
	a.Ino = ino
	a.Mode = syscall.S_IFREG | 0666
	a.Nlink = 1
	//a.Rdev
	a.Size = uint64(fi.Size)

	mtime := fi.ModTime()
	ctime := fi.InodeChangeTime()
	a.SetTimes(&mtime, &mtime, &ctime)

	return ffs.OK
}

func (f *virtualFuseFile) Getattr(ctx context.Context, a *fuse.AttrOut) syscall.Errno {
	fi, eno := f.getFileInfo()
	if eno != 0 {
		return eno
	}
	FileInfoToFuseAttrOut(fi, f.ino, a)
	return ffs.OK
}

func (f *virtualFuseFile) Lseek(ctx context.Context, off uint64, whence uint32) (uint64, syscall.Errno) {
	// TODO: does implementing this bring any significant advantage?
	return 0, syscall.ENOSYS
}

func (f *virtualFuseFile) Allocate(ctx context.Context, off uint64, sz uint64, mode uint32) syscall.Errno {
	// TODO
	return syscall.ENOSYS
}
