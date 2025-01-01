// Copyright (C) 2024 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/db"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/ignore"
	"github.com/syncthing/syncthing/lib/logger"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/semaphore"
	"github.com/syncthing/syncthing/lib/sync"
	"github.com/syncthing/syncthing/lib/utils"
	"github.com/syncthing/syncthing/lib/versioner"
)

func init() {
	log.SetFlags(log.Lmicroseconds)
	log.Default().SetOutput(os.Stdout)
	log.Default().SetPrefix("TESTLOG ")
}

type InitialScanState int

const (
	INITIAL_SCAN_IDLE      InitialScanState = iota
	INITIAL_SCAN_RUNNING   InitialScanState = iota
	INITIAL_SCAN_COMPLETED InitialScanState = iota
)

type virtualFolderSyncthingService struct {
	*folderBase
	lifetimeCtxCancel                 context.CancelFunc // TODO: when to call this function?
	mountPath                         string
	blobFs                            BlobFsI // blob FS needs to be early accessible as it is used to read the encryption token. TODO: when to close it?
	getBlockDataFromCacheOrDownloadFn func(file *protocol.FileInfo, block protocol.BlockInfo) ([]byte, error, GetBlockDataResult)

	running atomic.Pointer[runningVirtualFolderSyncthingService]
}

type runningVirtualFolderSyncthingService struct {
	parent            *virtualFolderSyncthingService
	blobFs            BlobFsI
	serviceRunningCtx context.Context
	doWorkCtx         context.Context

	backgroundDownloadQueue *jobQueue

	initialScanState InitialScanState
	InitialScanDone  chan struct{}

	puller atomic.Pointer[BlobFsScanOrPullI]
}

func (vFSS *virtualFolderSyncthingService) GetBlockDataFromCacheOrDownloadI(
	file *protocol.FileInfo,
	block protocol.BlockInfo,
) ([]byte, error, GetBlockDataResult) {
	return vFSS.getBlockDataFromCacheOrDownloadFn(file, block)
}

func (vFSS *virtualFolderSyncthingService) ReserveAndSetI(hash []byte, data []byte) {
	vFSS.blobFs.ReserveAndSetI(hash, data)
}

func NewVirtualFolder(
	model *model,
	fset *db.FileSet,
	ignores *ignore.Matcher,
	cfg config.FolderConfiguration,
	ver versioner.Versioner,
	evLogger events.Logger,
	ioLimiter *semaphore.Semaphore,
) service {

	lifetimeCtx, lifetimeCtxCancel := context.WithCancel(context.Background())

	folderBase := newFolderBase(cfg, evLogger, model, fset)

	parts := strings.Split(folderBase.Path, ":mount_at:")
	blobUrl := parts[0]
	mountPath := ""
	if len(parts) >= 2 {
		//url := "s3://bucket-syncthing-uli-virtual-folder-test1/" + myDir
		mountPath = parts[1]
	}

	blobFs := BlobFsI(nil)
	downloadFunction := (func(file *protocol.FileInfo, block protocol.BlockInfo) ([]byte, error, GetBlockDataResult))(nil)

	logger.DefaultLogger.Infof("newVirtualFolder(): create storage: %v, mount: %v", blobUrl, mountPath)

	remaining, hasResticPrefix := strings.CutPrefix(blobUrl, ":virtual::restic:")
	if hasResticPrefix {
		blobFs = model.blobFsRestic(
			lifetimeCtx,
			folderBase.ownDeviceIdString(),
			folderBase.folderID,
			remaining,
			folderBase.evLogger,
			fset)

		downloadFunction = func(file *protocol.FileInfo, block protocol.BlockInfo) ([]byte, error, GetBlockDataResult) {
			buf := make([]byte, block.Size)
			dataSize, err := blobFs.GetHashBlockData(lifetimeCtx, block.Hash, buf)
			if err == nil {
				return buf[:dataSize], nil, GET_BLOCK_CACHED
			}
			data, err := folderBase.pullBlockBaseConvenient(protocol.BlockOfFile{File: file, Block: block})
			if err == nil {
				return data, nil, GET_BLOCK_DOWNLOAD
			}
			return nil, err, GET_BLOCK_FAILED
		}
	} else {

		var blockCache HashBlockStorageI = model.blockStorageFactory(
			lifetimeCtx, blobUrl, folderBase.ownDeviceIdString())

		if folderBase.Type.IsReceiveEncrypted() {
			blockCache = NewEncryptedHashBlockStorage(blockCache)
		}

		blobFs = model.blobFsFactory(
			lifetimeCtx,
			folderBase.ownDeviceIdString(),
			folderBase.folderID,
			folderBase.evLogger,
			folderBase.fset,
			blockCache,
		)

		downloadFunction = func(file *protocol.FileInfo, block protocol.BlockInfo) ([]byte, error, GetBlockDataResult) {
			return GetBlockDataFromCacheOrDownload(blockCache, file, block, func(block protocol.BlockInfo) ([]byte, error) {
				return folderBase.pullBlockBaseConvenient(protocol.BlockOfFile{File: file, Block: block})
			}, false)
		}
	}

	f := &virtualFolderSyncthingService{
		folderBase:                        folderBase,
		lifetimeCtxCancel:                 lifetimeCtxCancel,
		mountPath:                         mountPath,
		blobFs:                            blobFs,
		getBlockDataFromCacheOrDownloadFn: downloadFunction,
		running:                           atomic.Pointer[runningVirtualFolderSyncthingService]{},
	}

	return f
}

func (vf *virtualFolderSyncthingService) runVirtualFolderServiceCoroutine(
	doWorkCtx context.Context,
	ping_pong_chan chan error, /* simulate coroutine */
) {

	initError := func() error { // coroutine

		running := vf.running.Load()
		if running != nil {
			return errors.New("internal error. virtual folder already running")
		}

		serviceRunningCtx, lifetimeCtxCancel := context.WithCancel(context.Background())
		defer lifetimeCtxCancel()

		jobQ := newJobQueue()
		running = &runningVirtualFolderSyncthingService{
			parent:                  vf,
			blobFs:                  vf.blobFs,
			serviceRunningCtx:       serviceRunningCtx,
			doWorkCtx:               doWorkCtx,
			backgroundDownloadQueue: jobQ,
			initialScanState:        INITIAL_SCAN_IDLE,
			InitialScanDone:         make(chan struct{}, 1),
		}
		vf.running.Store(running)

		backgroundDownloadTasks := vf.Copiers
		if backgroundDownloadTasks == 0 {
			backgroundDownloadTasks = 5
		}

		backgroundDownloadTaskWaitGroup := sync.NewWaitGroup()
		defer backgroundDownloadTaskWaitGroup.Wait()
		for i := 0; i < backgroundDownloadTasks; i++ {
			backgroundDownloadTaskWaitGroup.Add(1)
			go func() {
				defer backgroundDownloadTaskWaitGroup.Done()
				running.serve_backgroundDownloadTask()
			}()
		}
		defer jobQ.Close()

		if vf.mountPath != "" {
			stVF := NewSyncthingVirtualFolderFuseAdapter(
				vf.model.shortID,
				vf.ID,
				vf.Type,
				vf.fset,
				vf,
				vf,
			)

			mount, err := NewVirtualFolderMount(vf.mountPath, vf.ID, vf.Label, stVF)
			if err != nil {
				return err
			}

			defer func() {
				mount.Close()
			}()
		}

		if running.initialScanState == INITIAL_SCAN_IDLE {
			running.initialScanState = INITIAL_SCAN_RUNNING
			// TODO: rvf.Pull_x(ctx, PullOptions{false, true})
			running.initialScanState = INITIAL_SCAN_COMPLETED
			close(running.InitialScanDone)
			running.pullOrScan_x(doWorkCtx, PullOptions{true, false, false})
		}

		// unblock caller after successful init
		logger.DefaultLogger.Infof("Service coroutine running - unblock caller")
		ping_pong_chan <- nil

		logger.DefaultLogger.Infof("Service coroutine running - wait for shutdown signal")
		<-ping_pong_chan // wait for shutdown signal
		logger.DefaultLogger.Infof("Service coroutine running - shutdown signal received")

		return nil // all prepared defers needed for shutdown will be handled properly here
	}()

	ping_pong_chan <- initError // signal failed init (!= nil) or finalized shutdown (== nil)
	logger.DefaultLogger.Infof("Service coroutine shutdown - send DONE signal")
}

func (f *virtualFolderSyncthingService) RequestBackgroundDownloadI(
	filename string, size int64, modified time.Time, fn JobQueueProgressFn,
) {
	running := f.running.Load()
	if running == nil {
		panic("RequestBackgroundDownloadI() called on non-running virtual folder")
	}

	running.RequestBackgroundDownloadI(filename, size, modified, fn)
}

func (f *runningVirtualFolderSyncthingService) defaultJobWorkFn() func(job *jobQueueEntry) {
	return func(job *jobQueueEntry) {
		puller := *f.puller.Load()
		logger.DefaultLogger.Debugf("serve_backgroundDownloadTask: createVirtualFolderFilePullerAndPull, using puller: %p", puller)
		createVirtualFolderFilePullerAndPull(f.doWorkCtx, f, job, puller)
	}
}

func (f *runningVirtualFolderSyncthingService) RequestBackgroundDownloadI(
	filename string, size int64, modified time.Time, fn JobQueueProgressFn,
) {
	f.RequestBackgroundDownload(filename, size, modified, f.defaultJobWorkFn(), fn)
}

func (f *runningVirtualFolderSyncthingService) RequestBackgroundDownload(
	filename string, size int64, modified time.Time, workFn func(job *jobQueueEntry), fn JobQueueProgressFn,
) {
	wasNew := f.backgroundDownloadQueue.PushIfNew(filename, size, modified, workFn, fn)
	if !wasNew {
		if fn != nil {
			result := JobResultOK()
			result.skipped = true
			fn(size, result)
		}
		return
	}
}

func (f *runningVirtualFolderSyncthingService) serve_backgroundDownloadTask() {
	for {
		jobPtr, err := f.backgroundDownloadQueue.tryPopWithTimeout(time.Minute)
		if err != nil {
			return
		}

		if jobPtr == nil {
			continue
		}

		if utils.IsDone(f.doWorkCtx) {
			// empty queue as quick as possible
			f.backgroundDownloadQueue.Done(jobPtr.name)
			jobPtr.abort()
		} else {
			jobPtr.workFn(jobPtr)
		}
	}
}

// model.service API
func (f *virtualFolderSyncthingService) Serve(doWorkCtx context.Context) error {
	f.ctx = doWorkCtx // legacy compatibility

	f.model.foldersRunning.Add(1)
	defer f.model.foldersRunning.Add(-1)

	defer l.Infof("vf.Serve exits")

	co_chan := make(chan error) // un-buffered!
	go f.runVirtualFolderServiceCoroutine(doWorkCtx, co_chan)
	initError := <-co_chan
	if initError != nil {
		return initError
	} // else the service is initialized

	defer func() {
		// release service coroutine:
		logger.DefaultLogger.Infof("release service coroutine ...")
		co_chan <- nil
		logger.DefaultLogger.Infof("wait for stop of service coroutine ...")
		<-co_chan
		logger.DefaultLogger.Infof("service coroutine STOPPED")
	}()

	for {
		logger.DefaultLogger.Infof("virtualFolderServe: waiting for signal to process ...")
		select {
		case <-f.ctx.Done():
			close(f.done)
			l.Debugf("Serve: case <-ctx.Done()")
			return nil

		case req := <-f.doInSyncChan:
			l.Debugln(f, "Running something due to request")
			err := req.fn()
			req.err <- err
			continue

		case <-f.pullScheduled: // TODO: replace with "doInSyncChan"
			running := f.running.Load()
			if running == nil {
				continue
			}
			logger.DefaultLogger.Infof("virtualFolderServe: case <-f.pullScheduled")
			l.Debugf("Serve: f.pullAllMissing(false) - START")
			err := running.pullOrScan_x(doWorkCtx, PullOptions{true, false, false})
			l.Debugf("Serve: f.pullAllMissing(false) - DONE. Err: %v", err)
			logger.DefaultLogger.Infof("virtualFolderServe: case <-f.pullScheduled - 2")
			continue
		}
	}
}

func (f *virtualFolderSyncthingService) Override()                 {} // model.service API
func (f *virtualFolderSyncthingService) Revert()                   {} // model.service API
func (f *virtualFolderSyncthingService) DelayScan(d time.Duration) {} // model.service API

// model.service API
func (f *virtualFolderSyncthingService) ScheduleScan() {
	logger.DefaultLogger.Infof("ScheduleScan - pull_x")
	f.doInSync(func() error {
		running := f.running.Load()
		if running == nil {
			return nil // ignore request
		}
		err := running.pullOrScan_x(f.ctx, PullOptions{false, true, false})
		logger.DefaultLogger.Infof("ScheduleScan - pull_x - DONE. Err: %v", err)
		return err
	})
}

// model.service API
func (f *virtualFolderSyncthingService) Jobs(page, per_page uint) ([]string, []string, uint) {
	running := f.running.Load()
	if running == nil {
		return []string{}, []string{}, 0
	}
	return running.backgroundDownloadQueue.Jobs(uint(page), uint(per_page))
}

// model.service API
func (f *virtualFolderSyncthingService) BringToFront(filename string) {
	running := f.running.Load()
	if running == nil {
		return
	}

	running.backgroundDownloadQueue.BringToFront(filename)
}

// model.service API
func (vf *virtualFolderSyncthingService) Scan(subs []string) error {
	running := vf.running.Load()
	if running == nil {
		return fmt.Errorf("virtual folder not running")
	}

	logger.DefaultLogger.Infof("Scan(%+v) - pull_x", subs)
	return running.pullOrScan_x_doInSync(vf.ctx, PullOptions{false, true, false})
}

func (vf *virtualFolderSyncthingService) Validate(subs []string) error {
	running := vf.running.Load()
	if running == nil {
		return fmt.Errorf("virtual folder not running")
	}

	logger.DefaultLogger.Infof("Validate(%+v) - pull_x", subs)
	return running.pullOrScan_x_doInSync(vf.ctx, PullOptions{false, true, true})
}

func (f *runningVirtualFolderSyncthingService) pullOrScan_x_doInSync(doWorkCtx context.Context, opts PullOptions) error {
	logger.DefaultLogger.Infof("request pullOrScan_x_doInSync - %+v", opts)
	return f.parent.doInSync(func() error {
		logger.DefaultLogger.Infof("execute pullOrScan_x_doInSync - %+v", opts)
		return f.pullOrScan_x(doWorkCtx, opts)
	})
}

func (vf *runningVirtualFolderSyncthingService) pullOrScan_x(doWorkCtx context.Context, opts PullOptions) error {
	defer logger.DefaultLogger.Infof("pull_x END z - opts: %+v", opts)
	snap, err := vf.parent.fset.Snapshot()
	if err != nil {
		return err
	}
	defer logger.DefaultLogger.Infof("pull_x END snap - opts: %+v", opts)
	defer snap.Release()

	if opts.OnlyCheck {
		vf.parent.setState(FolderScanning)
	} else {
		vf.parent.setState(FolderSyncing)
	}
	defer logger.DefaultLogger.Infof("pull_x END setState - opts: %+v", opts)
	defer vf.parent.setState(FolderIdle)

	logger.DefaultLogger.Infof("pull_x START - opts: %+v", opts)
	defer logger.DefaultLogger.Infof("pull_x END a")

	serviceWorkerCtx, serviceWorkerCtxCancel := context.WithCancel(context.Background())
	defer serviceWorkerCtxCancel()
	scanner, err := vf.blobFs.StartScanOrPull(serviceWorkerCtx, opts)
	if err != nil {
		return err
	}
	vf.puller.Store(&scanner)
	defer func() {
		vf.puller.Store(nil)
	}()

	jobs := newJobQueue()
	totalBytes := uint64(0)
	{
		prepareFn := func(f protocol.FileInfo) bool {
			totalBytes += uint64(f.FileSize())
			jobs.Push(f.FileName(), f.FileSize(), f.ModTime())
			return true
		}

		if opts.OnlyMissing {
			snap.WithNeedTruncated(protocol.LocalDeviceID, prepareFn)
		} else {
			if opts.OnlyCheck {
				snap.WithHaveTruncated(protocol.LocalDeviceID, prepareFn)
			} else {
				snap.WithGlobalTruncated(prepareFn)
			}
		}

		jobs.SortAccordingToConfig(vf.parent.Order)
	}

	asyncNotifier := utils.NewAsyncProgressNotifier(vf.serviceRunningCtx)
	asyncNotifier.StartAsyncProgressNotification(
		logger.DefaultLogger, totalBytes, uint(1), vf.parent.evLogger, vf.parent.folderID, make([]string, 0), nil)
	defer logger.DefaultLogger.Infof("pull_x END asyncNotifier.Stop()")
	defer asyncNotifier.Stop()
	defer logger.DefaultLogger.Infof("pull_x END b")

	doScan := opts.OnlyCheck
	actionName := "Pull"
	if doScan {
		actionName = "Scan"
	}

	leaseCnt := uint(60)
	if !doScan {
		// unlimited for pull
		leaseCnt = math.MaxUint
	}
	leases := utils.NewParallelLeases(leaseCnt, actionName)
	defer leases.AbortAndWait()

	isAbortOrErr := false
	pullF := func(f protocol.FileInfo) bool /* true to continue */ {
		myFileSize := f.FileSize()
		workF := func(doneFn func()) {
			if !doScan {
				//logger.DefaultLogger.Infof("%v ONE - START, size: %v", actionName, myFileSize)
			}
			progressFn := func(deltaBytes int64, result *JobResult) {
				asyncNotifier.Progress.Update(deltaBytes)
				if result != nil {

					if result.Err == nil && opts.CheckData {
						buf := make([]byte, f.BlockSize())
						for _, block := range f.Blocks {
							dataLen, err := vf.blobFs.GetHashBlockData(doWorkCtx, block.Hash, buf)
							if err != nil {
								result.Err = err
								break
							}

							actualHashSum := sha256.Sum256(buf[:dataLen])
							if !bytes.Equal(actualHashSum[:], block.Hash) {
								vf.blobFs.ForceDropDataBlock(block.Hash)
								result.Err = fmt.Errorf("block hash mismatch")
							}
						}
					}

					doneFn()
					if !doScan {
						//logger.DefaultLogger.Infof("%v ONE - DONE, size: %v", actionName, myFileSize)
					}

					if result.Err != nil {
						if errors.Is(result.Err, ErrAborted) {
							return
						}
						// Revert means to throw away our local changes. We reset the
						// version to the empty vector, which is strictly older than any
						// other existing version. It is not in conflict with anything,
						// either, so we will not create a conflict copy of our local
						// changes.
						f.Version = protocol.Vector{}
						logger.DefaultLogger.Infof("%v reset version to zero, size: %v, err: %v", actionName, myFileSize, result.Err)
						vf.parent.fset.UpdateOne(protocol.LocalDeviceID, &f)
						// as this is NOT usual case, we don't store this to the meta data of block storage
						// NOT: updateOneLocalFileInfo(&fi)
					} else {
						// note: this also handles deletes
						// as the deleted file will not have any blocks,
						// the loop before is just skipped
						vf.parent.updateOneLocalFileInfo(&f, events.RemoteChangeDetected)

						seq := vf.parent.fset.Sequence(protocol.LocalDeviceID)
						vf.parent.evLogger.Log(events.LocalIndexUpdated, map[string]interface{}{
							"folder":    vf.parent.ID,
							"items":     1,
							"filenames": append([]string(nil), f.Name),
							"sequence":  seq,
							"version":   seq, // legacy for sequence
						})
					}
				}
			}
			err := error(nil)
			if opts.OnlyCheck {
				err = scanner.ScanOne(doWorkCtx, &f, progressFn)
			} else {
				// pull implementation is the same for all backends
				err = func() error {
					vf.parent.evLogger.Log(events.ItemStarted, map[string]string{
						"folder": vf.parent.folderID,
						"item":   f.FileName(),
						"type":   "file",
						"action": "update",
					})

					fn2 := func(deltaBytes int64, result *JobResult) {
						progressFn(deltaBytes, result)

						if result != nil {
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
						// not much work to do for directories. do it synchronously
						err := scanner.PullOne(doWorkCtx, &f, nil, nil)
						if err == nil {
							vf.parent.updateOneLocalFileInfo(&f, events.RemoteChangeDetected)
						}
						fn2(f.FileSize(), JobResultError(err))
					} else {
						vf.RequestBackgroundDownloadI(f.Name, f.Size, f.ModTime(), fn2)
					}

					return nil
				}()
			}

			if err != nil {
				logger.DefaultLogger.Infof("%v ONE - ERR, size: %v, err: %v", actionName, myFileSize, err)
				progressFn(0, JobResultError(err))
			}
		}

		leases.AsyncRunOneWithDoneFn(f.FileName(), workF)

		select {
		case <-doWorkCtx.Done():
			logger.DefaultLogger.Infof("pull ONE - stop continue")
			isAbortOrErr = true
			return false
		default:
			return true
		}
	}

	if isAbortOrErr {
		return nil
	}

	// do work:
	for job, ok := jobs.Pop(); ok; job, ok = jobs.Pop() {
		fi, ok := snap.GetGlobal(job.name)
		if ok {
			good := pullF(fi)
			if !good {
				isAbortOrErr = true
				break
			}
		}
	}

	leases.WaitAllFinished()
	logger.DefaultLogger.Infof("pull_x - leases.WaitAllFinished() - DONE")
	err = scanner.Finish(serviceWorkerCtx)
	logger.DefaultLogger.Infof("pull_x - scanner.Finish() - DONE, result: %v", err)
	if err != nil {
		logger.DefaultLogger.Warnf("pull_x - scanner.Finish() - ERR: %v", err)
	}

	if isAbortOrErr {
		return nil
	}

	if doScan {
		vf.parent.ScanCompleted()
	}

	return nil
}

func (vf *virtualFolderSyncthingService) updateOneLocalFileInfo(fi *protocol.FileInfo, typeOfEvent events.EventType) {
	vf.fset.UpdateOne(protocol.LocalDeviceID, fi)
	vf.ReceivedFile(fi.Name, fi.IsDeleted())
	vf.emitDiskChangeEvents([]protocol.FileInfo{*fi}, typeOfEvent)
	logger.DefaultLogger.Infof("VFolder: updateOneLocalFileInfo(%v): updated file info: %v, blocks: %v", typeOfEvent, fi.Name, len(fi.Blocks))
}

func (vf *virtualFolderSyncthingService) Update(fs []protocol.FileInfo) {
	vf.fset.Update(protocol.LocalDeviceID, fs) // TODO: check if store to blockCache meta needed
}

func (vf *virtualFolderSyncthingService) UpdateOneLocalFileInfoLocalChangeDetected(fi *protocol.FileInfo) {
	err := vf.blobFs.UpdateFile(vf.ctx, fi, func(block protocol.BlockInfo, status GetBlockDataResult) {},
		func(block protocol.BlockInfo) ([]byte, error) {
			panic("UpdateOneLocalFileInfoLocalChangeDetected(): download callback should not be called for already processed local block changes")
		})
	if err != nil {
		logger.DefaultLogger.Warnf("VFolder: UpdateOneLocalFileInfoLocalChangeDetected(): failed to update file. Err: %+v", err)
	}
	vf.updateOneLocalFileInfo(fi, events.LocalChangeDetected)
}

func (f *virtualFolderSyncthingService) Errors() []FileError             { return []FileError{} }
func (f *virtualFolderSyncthingService) WatchError() error               { return nil }
func (f *virtualFolderSyncthingService) ScheduleForceRescan(path string) {}

var _ = (virtualFolderServiceI)((*virtualFolderSyncthingService)(nil))

// API to model
func (vf *virtualFolderSyncthingService) GetHashBlockData(hash []byte, response_data []byte) (int, error) {
	return vf.blobFs.GetHashBlockData(vf.ctx, hash, response_data)
}

func (f *virtualFolderSyncthingService) ReadEncryptionToken() ([]byte, error) {
	data, err := f.blobFs.GetEncryptionToken()
	if err != nil {
		return nil, err
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
	f.blobFs.SetEncryptionToken(data.Bytes())
	return nil
}
