// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/syncthing/syncthing/lib/db"
	"github.com/syncthing/syncthing/lib/logger"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/sync"
)

type VirtualFolderFilePuller struct {
	sharedPullerStateBase
	job                     *jobQueueEntry
	folderService           *virtualFolderSyncthingService
	backgroundDownloadQueue *jobQueue
	fset                    *db.FileSet
	doWorkCtx               context.Context

	filePullerImpl BlobPullI
}

func createVirtualFolderFilePullerAndPull(
	doWorkCtx context.Context,
	f *runningVirtualFolderSyncthingService,
	job *jobQueueEntry,
	filePullerImpl BlobPullI,
) {
	defer f.backgroundDownloadQueue.Done(job.name)

	now := time.Now()

	fi, err := f.parent.fset.GetLatestGlobal(job.name)
	if err != nil {
		return
	}

	if filePullerImpl == nil {
		logger.DefaultLogger.Debugf("createVirtualFolderFilePullerAndPull: StartScanOrPull")
		filePullerImpl, err = f.blobFs.StartScanOrPull(f.serviceRunningCtx, PullOptions{OnlyMissing: false, OnlyCheck: false})
		if err != nil {
			return
		}
		defer func() {
			logger.DefaultLogger.Debugf("createVirtualFolderFilePullerAndPull: Finish")
			filePullerImpl.Finish(f.serviceRunningCtx)
		}()
	}

	logger.DefaultLogger.Debugf("createVirtualFolderFilePullerAndPull: UpdateFile, using puller: %p", filePullerImpl)

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
		folderService:           f.parent,
		backgroundDownloadQueue: f.backgroundDownloadQueue,
		fset:                    f.parent.fset,
		doWorkCtx:               doWorkCtx,
		filePullerImpl:          filePullerImpl,
	}

	f.parent.model.progressEmitter.Register(instance)
	defer f.parent.model.progressEmitter.Deregister(instance)

	instance.doPull()
}

func (f *VirtualFolderFilePuller) doPull() {

	all_ok := atomic.Bool{}
	all_ok.Store(true)

	err := f.filePullerImpl.PullOne(
		f.doWorkCtx,
		&f.file,
		func(bi protocol.BlockInfo, status GetBlockDataResult) {
			// blockStatusCb
			//logger.DefaultLogger.Debugf("VirtualFolderFilePuller-blockStatusCb: %v %v", bi, status)
			switch status {
			case GET_BLOCK_CACHED:
				f.copiedFromElsewhere(bi.Size)
				f.copyDone(bi)
			case GET_BLOCK_DOWNLOAD:
				f.pullDone(bi)
			case GET_BLOCK_FAILED:
				all_ok.Store(false)
			}

			fn := f.job.progressCb.Load()
			if fn != nil {
				(*fn)(int64(bi.Size), nil)
			}
		},
		func(block protocol.BlockInfo) ([]byte, error) {
			// downloadCb
			f.pullStarted()
			snap, err := f.fset.Snapshot()
			if err != nil {
				return nil, err
			}
			defer snap.Release()

			var data []byte = nil
			err = f.folderService.pullBlockBase(func(blockData []byte) {
				data = blockData
			}, snap, protocol.BlockOfFile{File: &f.file, Block: block})
			return data, err
		})

	if err != nil {
		f.job.Done(err)
		return
	}

	if !all_ok.Load() {
		f.job.Done(ErrMissingBlockData)
		return
	}

	f.job.Done(nil)
}
