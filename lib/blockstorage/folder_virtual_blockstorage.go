// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package blockstorage

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/db"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/hashutil"
	"github.com/syncthing/syncthing/lib/logger"
	"github.com/syncthing/syncthing/lib/model"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/utils"
	"google.golang.org/protobuf/proto"
)

const LOCAL_HAVE_FI_META_PREFIX = "LocalHaveMeta"

type BlockStorageFileBlobFs struct {
	ownDeviceID     string
	folderID        string
	evLogger        events.Logger
	fset            *db.FileSet
	blockDataAccess model.BlockDataAccessI

	blockCache    model.HashBlockStorageI
	deleteService *AsyncCheckedDeleteService
}

// ForceDropDataBlock implements model.BlobFsI.
func (vf *BlockStorageFileBlobFs) ForceDropDataBlock(hash []byte) {
	// TODO: implement
}

// ReadFileData implements model.BlobFsI.
func (vf *BlockStorageFileBlobFs) ReadFileData(ctx context.Context, name string) ([]byte, error) {
	panic("unimplemented")
}

type BlockStorageFileBlobFsPullOrScan struct {
	parent   *BlockStorageFileBlobFs
	scanCtx  context.Context
	checkMap model.HashBlockStateMap
	scanOpts model.PullOptions
	done     func()
}

func NewBlockStorageFileBlobFs(
	ctx context.Context,
	ownDeviceID string,
	folderID string,
	evLogger events.Logger,
	fset *db.FileSet,
	blockCache model.HashBlockStorageI,
) model.BlobFsI {

	return &BlockStorageFileBlobFs{
		ownDeviceID:   ownDeviceID,
		folderID:      folderID,
		evLogger:      evLogger,
		fset:          fset,
		blockCache:    blockCache,
		deleteService: NewAsyncCheckedDeleteService(ctx, blockCache),
	}
}

func (vf *BlockStorageFileBlobFs) Close() {
	vf.deleteService.Close()
}

// GetEncryptionToken implements model.BlobFsI.
func (vf *BlockStorageFileBlobFs) GetEncryptionToken() (data []byte, err error) {
	return vf.blockCache.GetMeta(config.EncryptionTokenName)
}

// SetEncryptionToken implements model.BlobFsI.
func (vf *BlockStorageFileBlobFs) SetEncryptionToken(data []byte) error {
	return vf.blockCache.SetMeta(config.EncryptionTokenName, data)
}

// StartScan implements BlobFsI.
func (vf *BlockStorageFileBlobFs) StartScanOrPull(
	ctx context.Context, opts model.PullOptions, done func(),
) (model.BlobFsScanOrPullI, error) {
	scanOrPull := &BlockStorageFileBlobFsPullOrScan{
		parent:   vf,
		scanCtx:  ctx,
		checkMap: nil,
		scanOpts: opts,
		done:     done,
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

// FinishScan implements BlobFsI.
func (b *BlockStorageFileBlobFsPullOrScan) Finish(workCtx context.Context) error {
	if b.checkMap != nil {
		b.parent.cleanupUnneededReservations(b.checkMap)
	}
	if b.done != nil {
		b.done()
	}
	return nil
}

func (vf *BlockStorageFileBlobFsPullOrScan) ScanOne(workCtx context.Context, fi protocol.FileInfo, progressFn model.JobQueueProgressFn) error {
	if vf.scanOpts.OnlyCheck {
		return vf.scanOne(vf.scanCtx, fi, progressFn)
	} else {
		panic("BlockStorageFileBlobFsPullOrScan::ScanOne(): should not be called for pull!")
	}
}

func (vf *BlockStorageFileBlobFsPullOrScan) PullOne(
	workCtx context.Context,
	fi protocol.FileInfo,
	blockStatusCb model.BlobPullStatusFn,
	downloadCb func(block protocol.BlockInfo) ([]byte, error),
) error {
	if vf.scanOpts.OnlyCheck && !vf.scanOpts.CheckData {
		panic("BlockStorageFileBlobFsPullOrScan::PullOne(): should not be called for scan!")
	} else {
		return vf.parent.UpdateFile(vf.scanCtx, fi, blockStatusCb, downloadCb)
	}
}

func (vf *BlockStorageFileBlobFsPullOrScan) scanOne(
	ctx context.Context, fi protocol.FileInfo, fn model.JobQueueProgressFn,
) error {

	if fi.IsDirectory() {
		// no work to do for directories.
		fn(fi.FileSize(), model.JobResultOK())
		return nil
	} else {
		return func() error {
			result := model.JobResultOK()
			defer fn(0, result)

			all_ok := true
			for _, bi := range fi.Blocks {
				//logger.DefaultLogger.Debugf("synchronous NEW check(%v) block info #%v: %+v", onlyCheck, i, bi, hashutil.HashToStringMapKey(bi.Hash))
				blockState, inMap := vf.checkMap[hashutil.HashToStringMapKey(bi.Hash)]
				ok := inMap
				if inMap && (!blockState.IsAvailableAndReservedByMe()) {
					// block is there but not hold, add missing hold - checking again for existence as in unhold state it could have been removed meanwhile
					_, err := vf.parent.blockCache.ReserveAndGet(bi.Hash, model.CHECK_ONLY)
					ok = (err == nil) // TODO: differentiate between error types
				}
				if !ok {
					logger.DefaultLogger.Debugf("synchronous cache-map based check(%v) failed for block info #%v: %+v, inMap: %v",
						fi.FileName(), bi.Offset, hashutil.HashToStringMapKey(bi.Hash), inMap)
				}
				all_ok = all_ok && ok

				fn(int64(bi.Size), nil)

				if utils.IsDone(vf.scanCtx) {
					return context.Canceled
				}
			}

			if !all_ok {
				//logger.DefaultLogger.Debugf("synchronous check block info result: incomplete, file: %s", fi.Name)
				result.Err = model.ErrMissingBlockData
			}

			return nil
		}()
	}
}

var _ = model.BlobFsI(&BlockStorageFileBlobFs{})

func (b *BlockStorageFileBlobFs) UpdateFile(
	ctx context.Context,
	fi protocol.FileInfo,
	blockStatusCb func(block protocol.BlockInfo, status model.GetBlockDataResult),
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

					_, err, status := model.GetBlockDataFromCacheOrDownload(
						b.blockCache, fi, bi, downloadBlockDataCb, model.CHECK_ONLY)

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

// ReserveAndSetI implements BlobFsI.
func (vf *BlockStorageFileBlobFs) ReserveAndSetI(hash []byte, data []byte) {
	vf.blockCache.ReserveAndSet(hash, data)
}

func (b *BlockStorageFileBlobFs) updateStoredFileMetadata(
	fi protocol.FileInfo,
) error {
	wireFi := fi.ToWire(false)
	fiData, err := proto.Marshal(wireFi)
	if err != nil {
		logger.DefaultLogger.Warnf("BlockStorageFileBlobFs: failed to serialize file info. Err: %+v", err)
		return err
	}

	metaKey := LOCAL_HAVE_FI_META_PREFIX + "/" +
		b.ownDeviceID + "/" +
		b.folderID + "/" +
		fi.Name
	b.blockCache.SetMeta(metaKey, fiData)
	logger.DefaultLogger.Debugf("BlockStorageFileBlobFs: Stored file info (size: %v) to %v", len(fiData), metaKey)

	return nil
}

func (vf *BlockStorageFileBlobFs) GetHashBlockData(ctx context.Context, hash []byte, response_data []byte) (int, error) {
	data, err := vf.blockCache.ReserveAndGet(hash, model.DOWNLOAD_DATA)
	if err != nil {
		return 0, err
	}
	n := copy(response_data, data)
	return n, nil
}

func (vf *BlockStorageFileBlobFs) cleanupUnneededReservations(checkMap model.HashBlockStateMap) error {
	closer := utils.NewCloser()
	defer closer.Close()

	snap, err := vf.fset.SnapshotInCloser(closer)
	if err != nil {
		return err
	}

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
