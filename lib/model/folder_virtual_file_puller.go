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
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/logger"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/sync"
)

type VirtualFolderFilePuller struct {
	sharedPullerStateBase
	job                     *jobQueueEntry
	snap                    *db.Snapshot
	folderService           *virtualFolderSyncthingService
	backgroundDownloadQueue *jobQueue
	fset                    *db.FileSet
	ctx                     context.Context

	filePullerImpl BlobFsScanOrPullI
}

func createVirtualFolderFilePullerAndPull(
	f *runningVirtualFolderSyncthingService,
	job *jobQueueEntry,
	filePullerImpl BlobFsI,
) {
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

	puller, err := filePullerImpl.StartScanOrPull(f.serviceRunningCtx, PullOptions{OnlyMissing: false, OnlyCheck: false})
	if err != nil {
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
		filePullerImpl:          puller,
	}

	f.parent.model.progressEmitter.Register(instance)
	defer f.parent.model.progressEmitter.Deregister(instance)

	instance.doPull()
}

func (f *VirtualFolderFilePuller) doPull() {

	all_ok := atomic.Bool{}
	all_ok.Store(true)

	err := f.filePullerImpl.PullOne(&f.file,
		func(bi protocol.BlockInfo, status GetBlockDataResult) {
			// blockStatusCb
			logger.DefaultLogger.Debugf("VirtualFolderFilePuller-blockStatusCb: %v %v", bi, status)
			switch status {
			case GET_BLOCK_CACHED:
				f.copiedFromElsewhere(bi.Size)
				f.copyDone(bi)
			case GET_BLOCK_DOWNLOAD:
				f.pullDone(bi)
			case GET_BLOCK_FAILED:
				all_ok.Store(false)
			}

			f.job.progressCb(int64(bi.Size), nil)
		},
		func(block protocol.BlockInfo) ([]byte, error) {
			// downloadCb
			f.pullStarted()
			var data []byte = nil
			err := f.folderService.pullBlockBase(func(blockData []byte) {
				data = blockData
			}, f.snap, protocol.BlockOfFile{File: &f.file, Block: block})
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

	// note: this also handles deletes
	// as the deleted file will not have any blocks,
	// the loop before is just skipped
	f.folderService.updateOneLocalFileInfo(&f.file, events.RemoteChangeDetected)

	f.job.Done(nil)

	seq := f.fset.Sequence(protocol.LocalDeviceID)
	f.folderService.evLogger.Log(events.LocalIndexUpdated, map[string]interface{}{
		"folder":    f.folderService.ID,
		"items":     1,
		"filenames": append([]string(nil), f.file.Name),
		"sequence":  seq,
		"version":   seq, // legacy for sequence
	})
}
