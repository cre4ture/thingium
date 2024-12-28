// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package blobfilefs

import (
	"context"

	"github.com/syncthing/syncthing/lib/protocol"
)

type GetBlockDataResult int

const (
	GET_BLOCK_FAILED   GetBlockDataResult = iota
	GET_BLOCK_CACHED   GetBlockDataResult = iota
	GET_BLOCK_DOWNLOAD GetBlockDataResult = iota
)

type BlobFsI interface {
	UpdateFile(
		ctx context.Context,
		fi *protocol.FileInfo,
		blockStatusCb func(block protocol.BlockInfo, status GetBlockDataResult),
		downloadBlockDataCb func(block protocol.BlockInfo) ([]byte, error),
	) error
}
