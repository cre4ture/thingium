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
	"github.com/syncthing/syncthing/lib/db"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/sync"
	"github.com/syncthing/syncthing/lib/utils"
)

type VirtualFolderFilePuller struct {
	sharedPullerStateBase
	job                     *jobQueueEntry
	snap                    *db.Snapshot
	folderService           *virtualFolderSyncthingService
	backgroundDownloadQueue *jobQueue
	fset                    *db.FileSet
	ctx                     context.Context

	filePullerImpl blobfilefs.BlobFsI
}

type BlockStorageFileBlobFs struct {
	folderService *virtualFolderSyncthingService
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
						b.folderService.blockCache, fi, bi, downloadBlockDataCb, true)

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
		b.folderService.evLogger.Log(events.Failure, fmt.Sprintf("failed to pull all blocks for: %v", fi.Name))
		return all_err.Load().(error)
	}

	return nil
}

func createVirtualFolderFilePullerAndPull(f *runningVirtualFolderSyncthingService, job *jobQueueEntry) {
	defer f.backgroundDownloadQueue.Done(job.name)

	now := time.Now()

	snap, err := f.parent.fset.Snapshot()
	if err != nil {
		return
	}
	defer snap.Release()

	fi, ok := snap.GetGlobal(job.name)
	if !ok {
		return
	}

	instance := &VirtualFolderFilePuller{
		sharedPullerStateBase: sharedPullerStateBase{
			created:          now,
			file:             fi,
			folder:           f.parent.folderID,
			reused:           0,
			copyTotal:        len(fi.Blocks),
			copyNeeded:       len(fi.Blocks),
			updated:          now,
			available:        make([]int, 0),
			availableUpdated: now,
			mut:              sync.NewRWMutex(),
		},
		job:                     job,
		snap:                    snap,
		folderService:           f.parent,
		backgroundDownloadQueue: f.backgroundDownloadQueue,
		fset:                    f.parent.fset,
		ctx:                     f.serviceRunningCtx,
		filePullerImpl:          &BlockStorageFileBlobFs{folderService: f.parent},
	}

	f.parent.model.progressEmitter.Register(instance)
	defer f.parent.model.progressEmitter.Deregister(instance)

	instance.doPull()
}

func (f *VirtualFolderFilePuller) doPull() {

	all_ok := atomic.Bool{}
	all_ok.Store(true)

	err := f.filePullerImpl.UpdateFile(f.ctx, &f.file,
		func(bi protocol.BlockInfo, status blobfilefs.GetBlockDataResult) {
			switch status {
			case blobfilefs.GET_BLOCK_CACHED:
				f.copiedFromElsewhere(bi.Size)
				f.copyDone(bi)
			case blobfilefs.GET_BLOCK_DOWNLOAD:
				f.pullDone(bi)
			case blobfilefs.GET_BLOCK_FAILED:
				all_ok.Store(false)
			}

			f.job.progressCb(int64(bi.Size), false)
		},
		func(block protocol.BlockInfo) ([]byte, error) {
			f.pullStarted()
			var data []byte = nil
			err := f.folderService.pullBlockBase(func(blockData []byte) {
				data = blockData
			}, f.snap, protocol.BlockOfFile{File: &f.file, Block: block})
			return data, err
		})

	if err != nil {
		return
	}

	if !all_ok.Load() {
		return
	}

	// note: this also handles deletes
	// as the deleted file will not have any blocks,
	// the loop before is just skipped
	f.folderService.updateOneLocalFileInfo(&f.file, events.RemoteChangeDetected)

	seq := f.fset.Sequence(protocol.LocalDeviceID)
	f.folderService.evLogger.Log(events.LocalIndexUpdated, map[string]interface{}{
		"folder":    f.folderService.ID,
		"items":     1,
		"filenames": append([]string(nil), f.file.Name),
		"sequence":  seq,
		"version":   seq, // legacy for sequence
	})
}
