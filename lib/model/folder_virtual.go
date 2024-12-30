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
	"errors"
	"log"
	"math"
	"os"
	"strings"
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

	running *runningVirtualFolderSyncthingService
}

type runningVirtualFolderSyncthingService struct {
	parent            *virtualFolderSyncthingService
	blobFs            BlobFsI
	serviceRunningCtx context.Context

	backgroundDownloadQueue *jobQueue

	initialScanState InitialScanState
	InitialScanDone  chan struct{}
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

	remaining, hasResticPrefix := strings.CutPrefix(blobUrl, ":restic:")
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
		running:                           nil,
	}

	return f
}

func (vf *virtualFolderSyncthingService) runVirtualFolderServiceCoroutine(
	ctx context.Context,
	ping_pong_chan chan error, /* simulate coroutine */
) {

	initError := func() error { // coroutine

		if vf.running != nil {
			return errors.New("internal error. virtual folder already running")
		}

		serviceRunningCtx, lifetimeCtxCancel := context.WithCancel(ctx)
		defer lifetimeCtxCancel()

		jobQ := newJobQueue()
		rvf := &runningVirtualFolderSyncthingService{
			parent:                  vf,
			blobFs:                  vf.blobFs,
			serviceRunningCtx:       serviceRunningCtx,
			backgroundDownloadQueue: jobQ,
			initialScanState:        INITIAL_SCAN_IDLE,
			InitialScanDone:         make(chan struct{}, 1),
		}
		vf.running = rvf

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
				vf.running.serve_backgroundDownloadTask()
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

		if rvf.initialScanState == INITIAL_SCAN_IDLE {
			rvf.initialScanState = INITIAL_SCAN_RUNNING
			// TODO: rvf.Pull_x(ctx, PullOptions{false, true})
			rvf.initialScanState = INITIAL_SCAN_COMPLETED
			close(rvf.InitialScanDone)
			rvf.pullOrScan_x(ctx, PullOptions{true, false})
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
	if f.running == nil {
		panic("RequestBackgroundDownloadI() called on non-running virtual folder")
	}

	f.running.RequestBackgroundDownloadI(filename, size, modified, fn)
}

func (f *runningVirtualFolderSyncthingService) RequestBackgroundDownloadI(
	filename string, size int64, modified time.Time, fn JobQueueProgressFn,
) {
	f.RequestBackgroundDownload(filename, size, modified, fn)
}

func (f *runningVirtualFolderSyncthingService) RequestBackgroundDownload(
	filename string, size int64, modified time.Time, fn JobQueueProgressFn,
) {
	wasNew := f.backgroundDownloadQueue.PushIfNew(filename, size, modified, fn)
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

		if utils.IsDone(f.serviceRunningCtx) {
			// empty queue as quick as possible
			f.backgroundDownloadQueue.Done(jobPtr.name)
			jobPtr.abort()
		} else {
			createVirtualFolderFilePullerAndPull(f, jobPtr, f.blobFs)
		}
	}
}

// model.service API
func (f *virtualFolderSyncthingService) Serve(ctx context.Context) error {
	f.ctx = ctx // legacy compatibility

	f.model.foldersRunning.Add(1)
	defer f.model.foldersRunning.Add(-1)

	defer l.Infof("vf.Serve exits")

	co_chan := make(chan error) // un-buffered!
	go f.runVirtualFolderServiceCoroutine(ctx, co_chan)
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
			logger.DefaultLogger.Infof("virtualFolderServe: case <-f.pullScheduled")
			l.Debugf("Serve: f.pullAllMissing(false) - START")
			err := f.running.pullOrScan_x(ctx, PullOptions{true, false})
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
		if f.running == nil {
			return nil // ignore request
		}
		err := f.running.pullOrScan_x(f.ctx, PullOptions{false, true})
		logger.DefaultLogger.Infof("ScheduleScan - pull_x - DONE. Err: %v", err)
		return err
	})
}

// model.service API
func (f *virtualFolderSyncthingService) Jobs(page, per_page uint) ([]string, []string, uint) {
	if f.running == nil {
		return []string{}, []string{}, 0
	}
	return f.running.backgroundDownloadQueue.Jobs(uint(page), uint(per_page))
}

// model.service API
func (f *virtualFolderSyncthingService) BringToFront(filename string) {
	if f.running == nil {
		return
	}

	f.running.backgroundDownloadQueue.BringToFront(filename)
}

// model.service API
func (vf *virtualFolderSyncthingService) Scan(subs []string) error {
	if vf.running == nil {
		return nil
	}

	logger.DefaultLogger.Infof("Scan(%+v) - pull_x", subs)
	return vf.running.pullOrScan_x_doInSync(vf.ctx, PullOptions{false, true})
}

func (f *runningVirtualFolderSyncthingService) pullOrScan_x_doInSync(ctx context.Context, opts PullOptions) error {
	logger.DefaultLogger.Infof("request pullOrScan_x_doInSync - %+v", opts)
	return f.parent.doInSync(func() error {
		logger.DefaultLogger.Infof("execute pullOrScan_x_doInSync - %+v", opts)
		return f.pullOrScan_x(ctx, opts)
	})
}

func (vf *runningVirtualFolderSyncthingService) pullOrScan_x(ctx context.Context, opts PullOptions) error {
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

	scanner, err := vf.blobFs.StartScanOrPull(ctx, opts)
	if err != nil {
		return err
	}

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
				logger.DefaultLogger.Infof("%v ONE - START, size: %v", actionName, myFileSize)
			}
			progressFn := func(deltaBytes int64, result *JobResult) {
				asyncNotifier.Progress.Update(deltaBytes)
				if result != nil {
					doneFn()
					if !doScan {
						logger.DefaultLogger.Infof("%v ONE - DONE, size: %v", actionName, myFileSize)
					}

					if result.Err != nil {
						// Revert means to throw away our local changes. We reset the
						// version to the empty vector, which is strictly older than any
						// other existing version. It is not in conflict with anything,
						// either, so we will not create a conflict copy of our local
						// changes.
						f.Version = protocol.Vector{}
						vf.parent.fset.UpdateOne(protocol.LocalDeviceID, &f)
						// as this is NOT usual case, we don't store this to the meta data of block storage
						// NOT: updateOneLocalFileInfo(&fi)
					}
				}
			}
			err := error(nil)
			if opts.OnlyCheck {
				err = scanner.ScanOne(&f, progressFn)
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
						// no work to do for directories.
						fn2(f.FileSize(), JobResultOK())
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
		case <-vf.serviceRunningCtx.Done():
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

	if isAbortOrErr {
		return nil
	}

	scanner.Finish()

	if doScan {
		vf.parent.ScanCompleted()
	}

	return nil
}

func (vf *virtualFolderSyncthingService) updateOneLocalFileInfo(fi *protocol.FileInfo, typeOfEvent events.EventType) {
	copy := *fi
	copy.Version = protocol.Vector{}
	//vf.fset.UpdateOne(protocol.LocalDeviceID, &copy)
	vf.fset.UpdateOne(protocol.LocalDeviceID, fi)
	vf.ReceivedFile(fi.Name, fi.IsDeleted())
	vf.emitDiskChangeEvents([]protocol.FileInfo{*fi}, typeOfEvent)
	logger.DefaultLogger.Infof("VFolder: updateOneLocalFileInfo(%v): updated file info: %v, blocks: %v", typeOfEvent, fi.Name, len(fi.Blocks))

	snap, err := vf.fset.Snapshot()
	if err != nil {
		return
	}
	defer snap.Release()
	global, hasGlobal := snap.GetGlobal(fi.Name)
	local, hasLocal := snap.Get(protocol.LocalDeviceID, fi.Name)

	logger.DefaultLogger.Infof("VFolder: updateOneLocalFileInfo(%v): stored: %+v", fi.Name, fi)
	logger.DefaultLogger.Infof("VFolder: updateOneLocalFileInfo(%v): global(%v): %+v", fi.Name, hasGlobal, global)
	logger.DefaultLogger.Infof("VFolder: updateOneLocalFileInfo(%v): local(%v): %+v", fi.Name, hasLocal, local)
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
