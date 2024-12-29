// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"context"
	"errors"

	"github.com/syncthing/syncthing/lib/protocol"
)

type GetBlockDataResult int

const (
	GET_BLOCK_FAILED   GetBlockDataResult = iota
	GET_BLOCK_CACHED   GetBlockDataResult = iota
	GET_BLOCK_DOWNLOAD GetBlockDataResult = iota
)

var ErrMissingBlockData = errors.New("missing block data")

type PullOptions struct {
	OnlyMissing bool
	OnlyCheck   bool
}

type BlobFsI interface {
	UpdateFile(
		ctx context.Context,
		fi *protocol.FileInfo,
		blockStatusCb func(block protocol.BlockInfo, status GetBlockDataResult),
		downloadBlockDataCb func(block protocol.BlockInfo) ([]byte, error),
	) error

	GetHashBlockData(ctx context.Context, hash []byte, response_data []byte) (int, error)
	ReserveAndSetI(hash []byte, data []byte)

	GetMeta(name string) (data []byte, err error)
	SetMeta(name string, data []byte) error

	StartScanOrPull(ctx context.Context, opts PullOptions) (BlobFsScanOrPullI, error)
}

type BlobFsScanOrPullI interface {
	DoOne(fi *protocol.FileInfo, fn JobQueueProgressFn) error
	Finish() error
}
