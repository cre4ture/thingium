package model

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/syncthing/syncthing/lib/db"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/sync"
	"github.com/syncthing/syncthing/lib/utils"
)

type VirtualFolderFilePuller struct {
	sharedPullerStateBase
	job                     jobQueueEntry
	snap                    *db.Snapshot
	folderService           *virtualFolderSyncthingService
	backgroundDownloadQueue *jobQueue
	fset                    *db.FileSet
	ctx                     context.Context
}

func createVirtualFolderFilePullerAndPull(f *virtualFolderSyncthingService, job jobQueueEntry) {
	defer f.backgroundDownloadQueue.Done(job.name)

	now := time.Now()

	snap, err := f.fset.Snapshot()
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

	all_ok := atomic.Bool{}
	all_ok.Store(true)
	func() {
		leases := utils.NewParallelLeases(10, 2)
		defer leases.WaitAllDone()

		for _, bi := range f.file.Blocks {
			//logger.DefaultLogger.Debugf("check block info #%v: %+v", i, bi)

			leases.AsyncRunOne(func() {
				f.pullStarted()
				_, ok, variant := f.folderService.GetBlockDataFromCacheOrDownload(f.snap, f.file, bi)
				if !ok {
					all_ok.Store(false)
				}

				switch variant {
				case GET_BLOCK_CACHED:
					f.copiedFromElsewhere(bi.Size)
					f.copyDone(bi)
				case GET_BLOCK_DOWNLOAD:
					f.pullDone(bi)
				case GET_BLOCK_FAILED:
				}

				f.job.progressCallback(int64(bi.Size), false)
			})

			if utils.IsDone(f.ctx) {
				return
			}
		}
	}()

	if utils.IsDone(f.ctx) {
		return
	}

	if !all_ok.Load() {
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
