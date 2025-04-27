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

type DirStream interface {
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
