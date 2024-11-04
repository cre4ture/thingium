// Copyright (C) 2024 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"strings"
	"time"

	"github.com/syncthing/syncthing/lib/blockstorage"
	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/db"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/hashutil"
	"github.com/syncthing/syncthing/lib/ignore"
	"github.com/syncthing/syncthing/lib/logger"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/semaphore"
	"github.com/syncthing/syncthing/lib/stats"
	"github.com/syncthing/syncthing/lib/sync"
	"github.com/syncthing/syncthing/lib/utils"
	"github.com/syncthing/syncthing/lib/versioner"
)

func init() {
	log.SetFlags(log.Lmicroseconds)
	log.Default().SetOutput(os.Stdout)
	log.Default().SetPrefix("TESTLOG ")
}

type virtualFolderSyncthingService struct {
	*folderBase
	blockCache   blockstorage.HashBlockStorageI
	mountPath    string
	mountService io.Closer

	backgroundDownloadPending chan struct{}
	backgroundDownloadQueue   jobQueue
}

func (vFSS *virtualFolderSyncthingService) GetBlockDataFromCacheOrDownload(
	snap *db.Snapshot,
	file protocol.FileInfo,
	block protocol.BlockInfo,
) ([]byte, bool) {
	data, ok := vFSS.blockCache.Get(block.Hash)
	if !ok {
		err := vFSS.pullBlockBase(func(blockData []byte) {
			data = blockData
		}, snap, file, block)

		if err != nil {
			return nil, false
		}

		vFSS.blockCache.Set(block.Hash, data)
	}

	return data, true
}

func newVirtualFolder(
	model *model,
	fset *db.FileSet,
	ignores *ignore.Matcher,
	cfg config.FolderConfiguration,
	ver versioner.Versioner,
	evLogger events.Logger,
	ioLimiter *semaphore.Semaphore,
) service {

	f := &virtualFolderSyncthingService{
		folderBase:                newFolderBase(cfg, evLogger, model, fset),
		blockCache:                nil,
		backgroundDownloadPending: make(chan struct{}, 1),
		backgroundDownloadQueue:   *newJobQueue(),
	}

	blobUrl := ""
	virtual_descriptor, hasVirtualDescriptor := strings.CutPrefix(f.Path, ":virtual:")
	if !hasVirtualDescriptor {
		panic("missing :virtual:")
	}

	parts := strings.Split(virtual_descriptor, ":mount_at:")
	blobUrl = parts[0]
	if len(parts) >= 2 {
		//url := "s3://bucket-syncthing-uli-virtual-folder-test1/" + myDir
		f.mountPath = parts[1]
	}

	f.blockCache = blockstorage.NewGoCloudUrlStorage(context.TODO(), blobUrl)

	return f
}

func (f *virtualFolderSyncthingService) RequestBackgroundDownload(filename string, size int64, modified time.Time, fn func()) {
	wasNew := f.backgroundDownloadQueue.PushIfNew(filename, size, modified, fn)
	if !wasNew {
		fn()
		return
	}

	f.backgroundDownloadQueue.SortAccordingToConfig(f.Order)
	select {
	case f.backgroundDownloadPending <- struct{}{}:
	default:
	}
}

func (f *virtualFolderSyncthingService) Serve_backgroundDownloadTask() {
	for {

		select {
		case <-f.ctx.Done():
			return
		case <-f.backgroundDownloadPending:
		}

		for job, ok := f.backgroundDownloadQueue.Pop(); ok; job, ok = f.backgroundDownloadQueue.Pop() {
			func() {
				defer f.backgroundDownloadQueue.Done(job)

				snap, err := f.fset.Snapshot()
				if err != nil {
					return
				}
				defer snap.Release()

				fi, ok := snap.GetGlobal(job)
				if !ok {
					return
				}

				all_ok := true
				for i, bi := range fi.Blocks {
					logger.DefaultLogger.Infof("check block info #%v: %+v", i, bi)
					_, ok := f.GetBlockDataFromCacheOrDownload(snap, fi, bi)
					all_ok = all_ok && ok
				}

				if !all_ok {
					f.evLogger.Log(events.Failure, fmt.Sprintf("failed to pull all blocks for: %v", job))
					return
				}

				f.fset.UpdateOne(protocol.LocalDeviceID, &fi)

				seq := f.fset.Sequence(protocol.LocalDeviceID)
				f.evLogger.Log(events.LocalIndexUpdated, map[string]interface{}{
					"folder":    f.ID,
					"items":     1,
					"filenames": append([]string(nil), fi.Name),
					"sequence":  seq,
					"version":   seq, // legacy for sequence
				})
			}()
		}
	}
}

func (f *virtualFolderSyncthingService) Serve(ctx context.Context) error {
	f.model.foldersRunning.Add(1)
	defer f.model.foldersRunning.Add(-1)

	f.ctx = ctx

	if (f.mountService == nil) && (f.mountPath != "") {
		stVF := &syncthingVirtualFolderFuseAdapter{
			vFSS:           f,
			folderID:       f.ID,
			model:          f.model,
			fset:           f.fset,
			ino_mu:         sync.NewMutex(),
			next_ino_nr:    1,
			ino_mapping:    make(map[string]uint64),
			directories_mu: sync.NewMutex(),
			directories:    make(map[string]*TreeEntry),
		}
		mount, err := NewVirtualFolderMount(f.mountPath, f.ID, f.Label, stVF)
		if err != nil {
			return err
		}

		f.mountService = mount
	}

	backgroundDownloadTasks := 40
	for i := 0; i < backgroundDownloadTasks; i++ {
		go f.Serve_backgroundDownloadTask()
	}

	for {
		select {
		case <-ctx.Done():
			if f.mountService != nil {
				f.mountService.Close()
				f.mountService = nil
			}
			if f.blockCache != nil {
				f.blockCache.Close()
				f.blockCache = nil
			}
			return nil

		case <-f.pullScheduled:
			f.PullAllMissing()
			continue
		}
	}
}

func (f *virtualFolderSyncthingService) Override()                 {}
func (f *virtualFolderSyncthingService) Revert()                   {}
func (f *virtualFolderSyncthingService) DelayScan(d time.Duration) {}
func (vf *virtualFolderSyncthingService) ScheduleScan() {
	vf.Scan([]string{})
}
func (f *virtualFolderSyncthingService) Jobs(page, per_page int) ([]string, []string, int) {
	return f.backgroundDownloadQueue.Jobs(page, per_page)
}
func (f *virtualFolderSyncthingService) BringToFront(filename string) {
	f.backgroundDownloadQueue.BringToFront(filename)
}

func (vf *virtualFolderSyncthingService) Scan(subs []string) error {
	return vf.PullAll()
}

func (vf *virtualFolderSyncthingService) PullAllMissing() error {
	return vf.Pull_x(true)
}

func (vf *virtualFolderSyncthingService) PullAll() error {
	return vf.Pull_x(false)
}

func (vf *virtualFolderSyncthingService) Pull_x(onlyMissing bool) error {
	snap, err := vf.fset.Snapshot()
	if err != nil {
		return err
	}
	defer snap.Release()

	vf.setState(FolderScanning)
	defer vf.setState(FolderIdle)

	logger.DefaultLogger.Infof("pull all START")

	count := 60
	inProgress := make(chan int, count)
	for i := 0; i < count; i++ {
		inProgress <- 100 + i
	}

	total := uint64(0)

	if onlyMissing {
		snap.WithNeedTruncated(protocol.LocalDeviceID, func(f protocol.FileIntf) bool {
			total += uint64(f.FileSize())
			return true
		})
	} else {
		snap.WithGlobalTruncated(func(f protocol.FileIntf) bool {
			total += uint64(f.FileSize())
			return true
		})
	}

	asyncNotifier := utils.NewAsyncProgressNotifier(vf.ctx)
	defer asyncNotifier.Progress.Close()
	asyncNotifier.StartAsyncProgressNotification(
		logger.DefaultLogger, total, uint(1), vf.evLogger, vf.folderID, make([]string, 0), nil)

	pullF := func(f protocol.FileIntf) bool /* true to continue */ {
		leaseNR := <-inProgress
		myFileSize := f.FileSize()
		go func() {
			logger.DefaultLogger.Infof("pull ONE with leaseNR: %v", leaseNR)
			vf.PullOne(snap, f, false, func() {
				asyncNotifier.Progress.Update(myFileSize)
				inProgress <- leaseNR
				logger.DefaultLogger.Infof("pull ONE with leaseNR: %v - DONE, size: %v", leaseNR, myFileSize)
			})
		}()
		return true
	}

	if onlyMissing {
		snap.WithNeedTruncated(protocol.LocalDeviceID, pullF)
	} else {
		snap.WithGlobalTruncated(pullF)
	}

	// wait for async operations to complete
	for i := 0; i < count; i++ {
		<-inProgress
	}

	logger.DefaultLogger.Infof("pull all END")

	return nil
}

func (vf *virtualFolderSyncthingService) PullOne(snap *db.Snapshot, f protocol.FileIntf, synchronous bool, fn func()) {
	if f.IsDirectory() {
		// no work to do for directories. directly take over:
		fi, ok := snap.GetGlobal(f.FileName())
		if ok {
			vf.fset.UpdateOne(protocol.LocalDeviceID, &fi)
		}
	} else {
		if !synchronous {
			vf.RequestBackgroundDownload(f.FileName(), f.FileSize(), f.ModTime(), fn)
		} else {
			defer fn()
			fi, ok := snap.GetGlobal(f.FileName())
			if ok {
				all_ok := true
				for i, bi := range fi.Blocks {
					logger.DefaultLogger.Infof("synchronous NEW check block info #%v: %+v", i, bi, hashutil.HashToStringMapKey(bi.Hash))
					_, ok := vf.GetBlockDataFromCacheOrDownload(snap, fi, bi)
					all_ok = all_ok && ok
					if !ok {
						logger.DefaultLogger.Warnf("synchronous check block info FAILED. NOT OK: #%v: %+v", i, bi, hashutil.HashToStringMapKey(bi.Hash))
					}
				}

				if all_ok {
					logger.DefaultLogger.Infof("synchronous check block info (%v blocks, %v size) SUCCEEDED. ALL OK, file: %s", fi.Blocks, fi.Size, fi.Name)
					vf.fset.UpdateOne(protocol.LocalDeviceID, &fi)
				} else {
					logger.DefaultLogger.Warnf("synchronous check block info FAILED. NOT ALL OK, file: %s", fi.Name)
				}
			}
		}
	}
}

func (f *virtualFolderSyncthingService) Errors() []FileError             { return []FileError{} }
func (f *virtualFolderSyncthingService) WatchError() error               { return nil }
func (f *virtualFolderSyncthingService) ScheduleForceRescan(path string) {}
func (f *virtualFolderSyncthingService) GetStatistics() (stats.FolderStatistics, error) {
	return stats.FolderStatistics{}, nil
}

var _ = (virtualFolderServiceI)((*virtualFolderSyncthingService)(nil))

func (vf *virtualFolderSyncthingService) GetHashBlockData(hash []byte, response_data []byte) (int, error) {
	if vf.blockCache == nil {
		return 0, protocol.ErrGeneric
	}
	data, ok := vf.blockCache.Get(hash)
	if !ok {
		return 0, protocol.ErrNoSuchFile
	}
	n := copy(response_data, data)
	return n, nil
}

func (f *virtualFolderSyncthingService) ReadEncryptionToken() ([]byte, error) {
	data, ok := f.blockCache.GetMeta(config.EncryptionTokenName)
	if !ok {
		return nil, fs.ErrNotExist
	}
	dataBuf := bytes.NewBuffer(data)
	var stored storedEncryptionToken
	if err := json.NewDecoder(dataBuf).Decode(&stored); err != nil {
		return nil, err
	}
	return stored.Token, nil
}
func (f *virtualFolderSyncthingService) WriteEncryptionToken(token []byte) error {
	data := bytes.Buffer{}
	err := json.NewEncoder(&data).Encode(storedEncryptionToken{
		FolderID: f.ID,
		Token:    token,
	})
	if err != nil {
		return err
	}
	f.blockCache.SetMeta(config.EncryptionTokenName, data.Bytes())
	return nil
}
