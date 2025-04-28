// Copyright (C) 2024 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"context"
	"syscall"
	"time"

	"github.com/syncthing/syncthing/lib/protocol"
)

type BlockDataAccessI interface {
	GetBlockDataFromCacheOrDownloadI(
		file protocol.FileInfo,
		block protocol.BlockInfo,
	) ([]byte, error, GetBlockDataResult)
	ReserveAndSetI(hash []byte, data []byte)
	RequestBackgroundDownloadI(filename string, size int64, modified time.Time, fn JobQueueProgressFn)
}

type ReadResult interface {
}

// DirEntry is a type for PathFileSystem and NodeFileSystem to return
// directory contents in.
type DirEntry struct {
	// Mode is the file's mode. Only the high bits (eg. S_IFDIR)
	// are considered.
	Mode uint32

	// Name is the basename of the file in the directory.
	Name string

	// Ino is the inode number.
	Ino uint64

	// Off is the offset in the directory stream. The offset is
	// thought to be after the entry.
	Off uint64
}

// DirStream lists directory entries.
type DirStream interface {
	// HasNext indicates if there are further entries. HasNext
	// might be called on already closed streams.
	HasNext() bool

	// Next retrieves the next entry. It is only called if HasNext
	// has previously returned true.  The Errno return may be used to
	// indicate I/O errors
	Next() (DirEntry, syscall.Errno)

	// Close releases resources related to this directory
	// stream.
	Close()
}

type SyncthingVirtualFolderAccessI interface {
	getInoOf(path string) uint64
	lookupFile(path string) (info *protocol.FileInfo, eno syscall.Errno)
	readDir(path string) (stream DirStream, eno syscall.Errno)
	readFile(path string, buf []byte, off int64) (res ReadResult, errno syscall.Errno)
	createFile(Permissions *uint32, path string) (info *protocol.FileInfo, eno syscall.Errno)
	writeFile(ctx context.Context, path string, offset uint64, inputData []byte) syscall.Errno
	deleteFile(ctx context.Context, path string) syscall.Errno
	createDir(ctx context.Context, path string) syscall.Errno
	deleteDir(ctx context.Context, path string) syscall.Errno
	renameFileOrDir(ctx context.Context, existingPath string, newPath string) syscall.Errno
	renameExchangeFileOrDir(ctx context.Context, path1 string, path2 string) syscall.Errno
	createSymlink(ctx context.Context, path, target string) syscall.Errno
}
