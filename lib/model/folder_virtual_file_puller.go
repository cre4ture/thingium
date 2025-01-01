// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"time"

	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/sync"
)

// Reports the state of a file puller.
type VirtualFolderFilePuller struct {
	sharedPullerStateBase
}

func runStatusReportingPull(
	f *runningVirtualFolderSyncthingService,
	fi protocol.FileInfo,
	pullFn func(blockStatusCb BlobPullStatusFn) error,
) error {
	now := time.Now()

	stateReporter := &VirtualFolderFilePuller{
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
	}

	f.parent.model.progressEmitter.Register(stateReporter)
	defer f.parent.model.progressEmitter.Deregister(stateReporter)

	return pullFn(func(bi protocol.BlockInfo, status GetBlockDataResult) {
		// blockStatusCb
		//logger.DefaultLogger.Debugf("VirtualFolderFilePuller-blockStatusCb: %v %v", bi, status)
		switch status {
		case GET_BLOCK_CACHED:
			stateReporter.copiedFromElsewhere(bi.Size)
			stateReporter.copyDone(bi)
		case GET_BLOCK_DOWNLOAD:
			stateReporter.pullDone(bi)
		case GET_BLOCK_FAILED:
			// ignore
		}
	})
}
