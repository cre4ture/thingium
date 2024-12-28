// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	blobfilefs "github.com/syncthing/syncthing/lib/blob_file_fs"
	"github.com/syncthing/syncthing/lib/blockstorage"
	"github.com/syncthing/syncthing/lib/logger"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/utils"
)

type BlockStorageFileBlobFs struct {
	blockCache blockstorage.HashBlockStorageI
}

func (b *BlockStorageFileBlobFs) UpdateFile(
	ctx context.Context,
	fi *protocol.FileInfo,
	blockStatusCb func(block protocol.BlockInfo, status blobfilefs.GetBlockDataResult),
	downloadBlockDataCb func(block protocol.BlockInfo) ([]byte, error),
) error {

	all_ok := atomic.Bool{}
	all_ok.Store(true)
	all_err := atomic.Value{}
	func() {
		leases := utils.NewParallelLeases(10, "BlockStorageFileBlobFs.UpdateFile")
		defer leases.AbortAndWait()

		for i, bi := range fi.Blocks {
			//logger.DefaultLogger.Debugf("check block info #%v: %+v", i, bi)

			leases.AsyncRunOne(fmt.Sprintf("%v:%v", fi.Name, i), func() {

				err := utils.AbortableTimeDelayedRetry(ctx, 6, time.Minute, func(tryNr uint) error {

					_, err, status := blockstorage.GetBlockDataFromCacheOrDownload(
						b.blockCache, fi, bi, downloadBlockDataCb, true)

					if err != nil {
						// trigger retry
						return err
					}

					blockStatusCb(bi, status)
					return err
				})

				if err != nil {
					all_ok.Store(false)
					all_err.Store(err)
				}
			})

			if utils.IsDone(ctx) {
				return
			}
		}
	}()

	if utils.IsDone(ctx) {
		return context.Canceled
	}

	if !all_ok.Load() {
		logger.DefaultLogger.Warnf("failed to pull all blocks for: %v", fi.Name)
		return all_err.Load().(error)
	}

	return nil
}
