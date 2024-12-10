package model

import (
	"context"
	"fmt"
	"time"

	"github.com/syncthing/syncthing/lib/db"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/sync"
)

type VirtualFolderFilePuller struct {
	sharedPullerStateBase
	job                     string
	snap                    *db.Snapshot
	folderService           *virtualFolderSyncthingService
	backgroundDownloadQueue *jobQueue
	fset                    *db.FileSet
	ctx                     context.Context
}

func createVirtualFolderFilePullerAndPull(f *virtualFolderSyncthingService, job string) {
	defer f.backgroundDownloadQueue.Done(job)

	now := time.Now()

	snap, err := f.fset.Snapshot()
	if err != nil {
		return
	}
	defer snap.Release()

	fi, ok := snap.GetGlobal(job)
	if !ok {
		return
	}

	instance := &VirtualFolderFilePuller{
		sharedPullerStateBase: sharedPullerStateBase{
			created:          now,
			file:             fi,
			folder:           f.folderID,
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
		folderService:           f,
		backgroundDownloadQueue: &f.backgroundDownloadQueue,
		fset:                    f.fset,
		ctx:                     f.ctx,
	}

	f.model.progressEmitter.Register(instance)
	defer f.model.progressEmitter.Deregister(instance)

	instance.doPull()
}

func (f *VirtualFolderFilePuller) doPull() {

	all_ok := true
	for _, bi := range f.file.Blocks {
		//logger.DefaultLogger.Debugf("check block info #%v: %+v", i, bi)
		_, ok, variant := f.folderService.GetBlockDataFromCacheOrDownload(f.snap, f.file, bi)
		all_ok = all_ok && ok

		switch variant {
		case GET_BLOCK_CACHED:
			f.copiedFromElsewhere(bi.Size)
			f.copyDone(bi)
		case GET_BLOCK_DOWNLOAD:
			f.pullDone(bi)
		case GET_BLOCK_FAILED:
		}

		select {
		case <-f.ctx.Done():
			return
		default:
		}
	}

	if !all_ok {
		f.folderService.evLogger.Log(events.Failure, fmt.Sprintf("failed to pull all blocks for: %v", f.job))
		return
	}

	// note: this also handles deletes
	// as the deleted file will not have any blocks,
	// the loop before is just skipped
	f.fset.UpdateOne(protocol.LocalDeviceID, &f.file)
	f.folderService.ReceivedFile(f.file.Name, f.file.Deleted)

	seq := f.fset.Sequence(protocol.LocalDeviceID)
	f.folderService.evLogger.Log(events.LocalIndexUpdated, map[string]interface{}{
		"folder":    f.folderService.ID,
		"items":     1,
		"filenames": append([]string(nil), f.file.Name),
		"sequence":  seq,
		"version":   seq, // legacy for sequence
	})
}
