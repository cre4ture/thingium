// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	blobfilefs "github.com/syncthing/syncthing/lib/blob_file_fs"
	"github.com/syncthing/syncthing/lib/blockstorage"
	"github.com/syncthing/syncthing/lib/db"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/hashutil"
	"github.com/syncthing/syncthing/lib/logger"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/utils"
	"google.golang.org/protobuf/proto"
)

type BlockStorageFileBlobFs struct {
	ownDeviceID     string
	folderID        string
	evLogger        events.Logger
	fset            *db.FileSet
	blockDataAccess BlockDataAccessI

	blockCache    blockstorage.HashBlockStorageI
	deleteService *blockstorage.AsyncCheckedDeleteService
}

type BlockStorageFileBlobFsPullOrScan struct {
	parent   *BlockStorageFileBlobFs
	scanCtx  context.Context
	checkMap blockstorage.HashBlockStateMap
	scanOpts blobfilefs.PullOptions
}

func NewBlockStorageFileBlobFs(
	ctx context.Context,
	ownDeviceID string,
	folderID string,
	evLogger events.Logger,
	fset *db.FileSet,
	blockCache blockstorage.HashBlockStorageI,
) *BlockStorageFileBlobFs {

	return &BlockStorageFileBlobFs{
		ownDeviceID:   ownDeviceID,
		folderID:      folderID,
		evLogger:      evLogger,
		fset:          fset,
		blockCache:    blockCache,
		deleteService: blockstorage.NewAsyncCheckedDeleteService(ctx, blockCache),
	}
}

func (vf *BlockStorageFileBlobFs) Close() {
	vf.deleteService.Close()
}

// GetMeta implements blobfilefs.BlobFsI.
func (b *BlockStorageFileBlobFs) GetMeta(name string) (data []byte, err error) {
	return b.blockCache.GetMeta(name)
}

// SetMeta implements blobfilefs.BlobFsI.
func (b *BlockStorageFileBlobFs) SetMeta(name string, data []byte) error {
	return b.blockCache.SetMeta(name, data)
}

// StartScan implements blobfilefs.BlobFsI.
func (vf *BlockStorageFileBlobFs) StartScanOrPull(
	ctx context.Context, opts blobfilefs.PullOptions,
) (blobfilefs.BlobFsScanOrPullI, error) {
	scanOrPull := &BlockStorageFileBlobFsPullOrScan{
		parent:   vf,
		scanCtx:  ctx,
		checkMap: nil,
		scanOpts: opts,
	}
	if opts.OnlyCheck {
		err := func() error {
			asyncNotifier := utils.NewAsyncProgressNotifier(ctx)
			asyncNotifier.StartAsyncProgressNotification(
				logger.DefaultLogger,
				uint64(255), // use first hash byte as progress indicator. This works as storage is sorted.
				uint(5),
				vf.evLogger,
				vf.folderID,
				make([]string, 0),
				nil)
			defer logger.DefaultLogger.Infof("pull_x END1 asyncNotifier.Stop()")
			defer asyncNotifier.Stop()

			err := error(nil)
			scanOrPull.checkMap, err = vf.blockCache.GetBlockHashesCache(ctx, func(count int, currentHash []byte) {
				if len(currentHash) < 1 {
					log.Panicf("Scan progress: Length of currentHash is zero! %v", currentHash)
				}
				progressByte := uint64(currentHash[0])
				// logger.DefaultLogger.Infof("GetBlockHashesCache - progress: %v, byte: 0x%x", count, progressByte)
				asyncNotifier.Progress.UpdateTotal(progressByte)
			})
			return err
		}()

		if err != nil {
			return nil, err
		}
	}

	return scanOrPull, nil
}

// FinishScan implements blobfilefs.BlobFsI.
func (b *BlockStorageFileBlobFsPullOrScan) Finish() error {
	if b.checkMap != nil {
		b.parent.cleanupUnneededReservations(b.checkMap)
	}
	return nil
}

func (vf *BlockStorageFileBlobFsPullOrScan) DoOne(fi *protocol.FileInfo, progressFn blobfilefs.JobQueueProgressFn) error {
	if vf.scanOpts.OnlyCheck {
		return vf.scanOne(vf.scanCtx, fi, progressFn)
	} else {
		return vf.pullOne(vf.scanCtx, fi, progressFn)
	}
}

func (vf *BlockStorageFileBlobFsPullOrScan) pullOne(
	ctx context.Context, f *protocol.FileInfo, fn blobfilefs.JobQueueProgressFn,
) error {

	vf.parent.evLogger.Log(events.ItemStarted, map[string]string{
		"folder": vf.parent.folderID,
		"item":   f.FileName(),
		"type":   "file",
		"action": "update",
	})

	err := error(nil)

	fn2 := func(deltaBytes int64, done bool) {
		fn(deltaBytes, done)

		if done {
			vf.parent.evLogger.Log(events.ItemFinished, map[string]interface{}{
				"folder": vf.parent.folderID,
				"item":   f.FileName(),
				"error":  events.Error(err),
				"type":   "dir",
				"action": "update",
			})
		}
	}

	if f.IsDirectory() {
		// no work to do for directories.
		fn2(f.FileSize(), true)
	} else {
		vf.parent.blockDataAccess.RequestBackgroundDownloadI(f.Name, f.Size, f.ModTime(), fn2)
	}

	return nil
}

func (vf *BlockStorageFileBlobFsPullOrScan) scanOne(
	ctx context.Context, fi *protocol.FileInfo, fn blobfilefs.JobQueueProgressFn,
) error {

	if fi.IsDirectory() {
		// no work to do for directories.
		fn(fi.FileSize(), true)
		return nil
	} else {
		return func() error {
			defer fn(0, true)

			all_ok := true
			for _, bi := range fi.Blocks {
				//logger.DefaultLogger.Debugf("synchronous NEW check(%v) block info #%v: %+v", onlyCheck, i, bi, hashutil.HashToStringMapKey(bi.Hash))
				blockState, inMap := vf.checkMap[hashutil.HashToStringMapKey(bi.Hash)]
				ok := inMap
				if inMap && (!blockState.IsAvailableAndReservedByMe()) {
					// block is there but not hold, add missing hold - checking again for existence as in unhold state it could have been removed meanwhile
					_, err := vf.parent.blockCache.ReserveAndGet(bi.Hash, false)
					ok = (err == nil) // TODO: differentiate between error types
				}
				if !ok {
					logger.DefaultLogger.Debugf("synchronous cache-map based check(%v) failed for block info #%v: %+v, inMap: %v",
						fi.FileName(), bi.Offset, hashutil.HashToStringMapKey(bi.Hash), inMap)
				}
				all_ok = all_ok && ok

				fn(int64(bi.Size), false)

				if utils.IsDone(vf.scanCtx) {
					return context.Canceled
				}
			}

			if !all_ok {
				//logger.DefaultLogger.Debugf("synchronous check block info result: incomplete, file: %s", fi.Name)
				// Revert means to throw away our local changes. We reset the
				// version to the empty vector, which is strictly older than any
				// other existing version. It is not in conflict with anything,
				// either, so we will not create a conflict copy of our local
				// changes.
				fi.Version = protocol.Vector{}
				vf.parent.fset.UpdateOne(protocol.LocalDeviceID, fi)
				// as this is NOT usual case, we don't store this to the meta data of block storage
				// NOT: updateOneLocalFileInfo(&fi)
			}

			return nil
		}()
	}
}

var _ = blobfilefs.BlobFsI(&BlockStorageFileBlobFs{})

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

	err := b.updateStoredFileMetadata(fi)
	if err != nil {
		return err
	}

	return nil
}

// ReserveAndSetI implements blobfilefs.BlobFsI.
func (vf *BlockStorageFileBlobFs) ReserveAndSetI(hash []byte, data []byte) {
	vf.blockCache.ReserveAndSet(hash, data)
}

func (b *BlockStorageFileBlobFs) updateStoredFileMetadata(
	fi *protocol.FileInfo,
) error {
	wireFi := fi.ToWire(false)
	fiData, err := proto.Marshal(wireFi)
	if err != nil {
		logger.DefaultLogger.Warnf("BlockStorageFileBlobFs: failed to serialize file info. Err: %+v", err)
		return err
	}

	metaKey := blockstorage.LOCAL_HAVE_FI_META_PREFIX + "/" +
		b.ownDeviceID + "/" +
		b.folderID + "/" +
		fi.Name
	b.blockCache.SetMeta(metaKey, fiData)
	logger.DefaultLogger.Debugf("BlockStorageFileBlobFs: Stored file info (size: %v) to %v", len(fiData), metaKey)

	return nil
}

func (vf *BlockStorageFileBlobFs) GetHashBlockData(ctx context.Context, hash []byte, response_data []byte) (int, error) {
	data, err := vf.blockCache.ReserveAndGet(hash, true)
	if err != nil {
		return 0, err
	}
	n := copy(response_data, data)
	return n, nil
}

func (vf *BlockStorageFileBlobFs) cleanupUnneededReservations(checkMap blockstorage.HashBlockStateMap) error {
	snap, err := vf.fset.Snapshot()
	if err != nil {
		return err
	}
	defer logger.DefaultLogger.Infof("cleanupUnneeded END snap")
	defer snap.Release()

	dummyValue := struct{}{}
	usedBlockHashes := map[string]struct{}{}
	snap.WithHave(protocol.LocalDeviceID, func(f protocol.FileInfo) bool {
		fi, ok := snap.Get(protocol.LocalDeviceID, f.FileName())
		if !ok {
			log.Panicf("cleanupUnneeded: inconsistent snapshot! %v", f.FileName())
		}
		for _, bi := range fi.Blocks {
			usedBlockHashes[hashutil.HashToStringMapKey(bi.Hash)] = dummyValue
		}
		return true
	})

	for hash, state := range checkMap {
		if state.IsAvailableAndFree() {
			byteHash := hashutil.StringMapKeyToHashNoError(hash)
			vf.deleteService.RequestCheckedDelete(byteHash)
		} else if state.IsAvailableAndReservedByMe() {
			_, stillNeeded := usedBlockHashes[hash]
			if !stillNeeded {
				byteHash := hashutil.StringMapKeyToHashNoError(hash)
				vf.blockCache.DeleteReservation(byteHash)
				vf.deleteService.RequestCheckedDelete(byteHash)
			}
		}
	}

	return nil
}
