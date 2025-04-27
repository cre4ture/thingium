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
	"io"
	"log"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
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
	//log.Default().SetOutput(os.Stdout)
	log.Default().SetOutput(io.Discard)
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
	getBlockDataFromCacheOrDownloadFn func(file protocol.FileInfo, block protocol.BlockInfo) ([]byte, error, GetBlockDataResult)

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
}

func (vFSS *virtualFolderSyncthingService) GetBlockDataFromCacheOrDownloadI(
	file protocol.FileInfo,
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
	downloadFunction := (func(file protocol.FileInfo, block protocol.BlockInfo) ([]byte, error, GetBlockDataResult))(nil)

	logger.DefaultLogger.Infof("newVirtualFolder(): create storage: %v, mount: %v", blobUrl, mountPath)

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

	downloadFunction = func(file protocol.FileInfo, block protocol.BlockInfo) ([]byte, error, GetBlockDataResult) {
		return GetBlockDataFromCacheOrDownload(blockCache, file, block, func(block protocol.BlockInfo) ([]byte, error) {
			return folderBase.pullBlockBaseConvenient(protocol.BlockOfFile{File: file, Block: block})
		}, DOWNLOAD_DATA)
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
func (f *runningVirtualFolderSyncthingService) RequestBackgroundDownloadI(
	filename string, size int64, modified time.Time, fn JobQueueProgressFn,
) {
	fi, err := f.parent.fset.GetLatestGlobal(filename)
	if err != nil {
		return
	}

	f.RequestBackgroundDownload(filename, size, modified, fn,
		func(job *jobQueueEntry) error {
			return runStatusReportingPull(f, fi,
				func(blockStatusCb BlobPullStatusFn) error {
					closer := utils.NewCloser()
					defer closer.Close()

					snap, err := f.parent.fset.SnapshotInCloser(closer)
					if err != nil {
						return err
					}

					return f.blobFs.UpdateFile(f.doWorkCtx, fi, blockStatusCb,
						func(block protocol.BlockInfo) ([]byte, error) {
							return f.parent.pullBlockBaseConvenientSnap(snap, protocol.BlockOfFile{File: fi, Block: block})
						})
				})
		})
}

func (f *runningVirtualFolderSyncthingService) RequestBackgroundDownload(
	filename string, size int64, modified time.Time, fn JobQueueProgressFn, workFn JobQueueWorkFn,
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
			func() {
				workFn := jobPtr.workFn.Swap(nil)
				err := error(nil)
				defer func() {
					jobPtr.Done(err)
				}()
				if workFn != nil {
					err = (*workFn)(jobPtr)
				}
			}()
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

func (vf *runningVirtualFolderSyncthingService) generateSortedJobList(opts PullOptions) (*jobQueue, uint64, error) {

	closer := utils.NewCloser()
	defer closer.Close()

	snap, err := vf.parent.fset.SnapshotInCloser(closer)
	if err != nil {
		return nil, 0, err
	}

	jobs := newJobQueue()
	totalBytes := uint64(0)

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

	closer.Close()

	jobs.SortAccordingToConfig(vf.parent.Order)

	return jobs, totalBytes, nil
}

type DataCheckerScan struct {
	fs   BlobFsI
	impl BlobFsScanI
}

// Finish implements BlobPullI.
func (d *DataCheckerScan) Finish(workCtx context.Context) error {
	return d.impl.Finish(workCtx)
}

var (
	syncthing_file_scan_duration = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "syncthing_file_scan_duration",
		Help: "The total number of blocks validated",
	})
	syncthing_scanned_files_total = promauto.NewCounter(prometheus.CounterOpts{
		Name: "syncthing_scanned_files_total",
		Help: "The total number of files scanned",
	})
	syncthing_scanned_files_total2 = promauto.NewCounter(prometheus.CounterOpts{
		Name: "syncthing_scanned_files_total2",
		Help: "The total number of files scanned",
	})
	syncthing_queued_for_scan_files_total = promauto.NewCounter(prometheus.CounterOpts{
		Name: "syncthing_queued_for_scan_files_total",
		Help: "The total number of files scanned",
	})
	syncthing_validated_blocks_total = promauto.NewCounter(prometheus.CounterOpts{
		Name: "syncthing_validated_blocks_total",
		Help: "The total number of blocks validated",
	})
	syncthing_queued_for_validation_blocks_total = promauto.NewCounter(prometheus.CounterOpts{
		Name: "syncthing_queued_for_validation_blocks_total",
		Help: "The total number of blocks validated",
	})
)

type ErrorCollector struct {
	err atomic.Pointer[error]
}

func NewErrorCollector() *ErrorCollector {
	return &ErrorCollector{
		err: atomic.Pointer[error]{},
	}
}

func (ec *ErrorCollector) Collect(err error) {
	ec.err.CompareAndSwap(nil, &err)
}

func (ec *ErrorCollector) Error() error {
	return *ec.err.Load()
}

// PullOne implements BlobPullI.
func (d *DataCheckerScan) ScanOne(workCtx context.Context, fi protocol.FileInfo, progressReportedFn JobQueueProgressFn) error {

	start := time.Now()
	collector := NewErrorCollector()
	sg := utils.NewSafeWorkGroup(workCtx, 11)
	sg.Run(func(done func(), ctx context.Context) {
		syncthing_queued_for_scan_files_total.Inc()
		go func() {
			defer syncthing_scanned_files_total.Inc()
			collector.Collect(d.impl.ScanOne(workCtx, fi, func(deltaBytes int64, result *JobResult) {
				progressReportedFn(deltaBytes, nil)
				if result != nil {
					collector.Collect(result.Err)
					syncthing_scanned_files_total2.Inc()
					end := time.Now()
					syncthing_file_scan_duration.Set(float64(end.Sub(start).Milliseconds()))
					done()
				}
			}))
		}()
	})

	for _, block := range fi.Blocks {
		sg.Go(func(ctx context.Context) {
			syncthing_queued_for_validation_blocks_total.Inc()
			defer syncthing_validated_blocks_total.Inc()
			buf := make([]byte, block.Size)
			n, errData := d.fs.GetHashBlockData(workCtx, block.Hash, buf)
			if errData == nil {
				actualHashSum := sha256.Sum256(buf[:n])
				if !bytes.Equal(actualHashSum[:], block.Hash) {
					errData = fmt.Errorf("block hash mismatch")
				}
			}
			progressReportedFn(int64(block.Size), nil)
			collector.Collect(errData)
		})
	}

	sg.CloseAndWait()

	err := collector.Error()
	progressReportedFn(0, JobResultError(err))
	return err
}

func (vf *runningVirtualFolderSyncthingService) pullOrScan_x(doWorkCtx context.Context, opts PullOptions) error {
	defer logger.DefaultLogger.Infof("pull_x END z - opts: %+v", opts)
	closer := utils.NewCloser()
	defer closer.Close()
	err := error(nil)

	defer logger.DefaultLogger.Infof("pull_x END snap - opts: %+v", opts)

	if opts.OnlyCheck {
		vf.parent.setState(FolderScanning)
	} else {
		vf.parent.setState(FolderSyncing)
	}
	defer logger.DefaultLogger.Infof("pull_x END setState - opts: %+v", opts)
	defer vf.parent.setState(FolderIdle)

	logger.DefaultLogger.Infof("pull_x START - opts: %+v", opts)
	defer logger.DefaultLogger.Infof("pull_x END a")

	serviceObject := utils.NewServingObject(doWorkCtx, vf, 0)

	scanPuller := BlobFsScanOrPullI(nil)
	serviceObject.ServiceRoutineRun(func(obj *runningVirtualFolderSyncthingService, done func(), ctx context.Context) {
		scanPuller, err = vf.blobFs.StartScanOrPull(ctx, opts, done)
		if err != nil {
			done()
		}
	})
	if err != nil {
		return err
	}

	jobs, totalBytes, err := vf.generateSortedJobList(opts)
	if err != nil {
		return err
	}

	scanner := scanPuller.(BlobFsScanI)
	puller := scanPuller.(BlobPullI)
	actionName := ""
	if opts.OnlyCheck {
		puller = nil
		if opts.CheckData {
			actionName = "ScanValidate"
			scanner = &DataCheckerScan{fs: vf.blobFs, impl: scanner}
			totalBytes = totalBytes * 2 // scanned for hash AND data
		} else {
			actionName = "Scan"
		}
	} else {
		actionName = "Pull"
		scanner = nil
	}

	asyncNotifier := utils.NewAsyncProgressNotifier(vf.serviceRunningCtx)
	asyncNotifier.StartAsyncProgressNotification(
		logger.DefaultLogger, totalBytes, uint(1), vf.parent.evLogger, vf.parent.folderID, make([]string, 0), nil)
	defer logger.DefaultLogger.Infof("pull_x END asyncNotifier.Stop()")
	defer asyncNotifier.Stop()
	defer logger.DefaultLogger.Infof("pull_x END b")

	leaseCnt := uint(60)
	if puller != nil {
		// unlimited for pull
		leaseCnt = math.MaxUint
	}
	if opts.CheckData {
		// check data is more expensive
		leaseCnt = 10
	}
	leases := utils.NewSafeWorkGroup(doWorkCtx, int(leaseCnt))
	defer leases.CloseAndWait()

	isAbortOrErr := false
	pullF := func(f protocol.FileInfo) bool /* true to continue */ {
		myFileSize := f.FileSize()
		workF := func(doneFn func()) {

			progressFn := func(deltaBytes int64, result *JobResult) {
				asyncNotifier.Progress.Update(deltaBytes)
				if result != nil {

					doneFn()

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
						if !opts.OnlyCheck {
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
			}

			err := error(nil)
			if scanner != nil {
				//start := time.Now()
				err = scanner.ScanOne(doWorkCtx, f, func(deltaBytes int64, result *JobResult) {
					//if result != nil {
					//	//time.Sleep(5 * time.Millisecond)
					//	//end := time.Now()
					//	//syncthing_file_scan_duration.Set(float64(end.Sub(start).Milliseconds()))
					//}
					progressFn(deltaBytes, result)
				})
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
						err := puller.PullOne(doWorkCtx, f, nil, nil)
						if err == nil {
							vf.parent.updateOneLocalFileInfo(&f, events.RemoteChangeDetected)
						}
						fn2(f.FileSize(), JobResultError(err))
					} else {
						// for files, we do the work as part of the background download queue
						vf.RequestBackgroundDownload(f.Name, f.Size, f.ModTime(), fn2,
							func(job *jobQueueEntry) error {
								closer := utils.NewCloser()
								defer closer.Close()

								snap, err := vf.parent.fset.SnapshotInCloser(closer)
								if err != nil {
									return err
								}

								fi, ok := snap.GetGlobal(job.name)
								if !ok {
									return protocol.ErrNoSuchFile
								}
								return runStatusReportingPull(vf, fi,
									func(blockStatusCb BlobPullStatusFn) error {
										return puller.PullOne(doWorkCtx, fi,
											func(block protocol.BlockInfo, status GetBlockDataResult) {
												blockStatusCb(block, status)
												progressFn(int64(block.Size), nil)
											},
											func(block protocol.BlockInfo) ([]byte, error) {
												return vf.parent.pullBlockBaseConvenientSnap(snap, protocol.BlockOfFile{File: fi, Block: block})
											},
										)
									})
							},
						)
					}

					return nil
				}()
			}

			if err != nil {
				logger.DefaultLogger.Infof("%v ONE - ERR, size: %v, err: %v", actionName, myFileSize, err)
				progressFn(0, JobResultError(err))
			}
		}

		leases.Run(func(done func(), ctx context.Context) {
			go workF(done)
		})

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

	snap, err := vf.parent.fset.SnapshotInCloser(closer)
	if err != nil {
		return err
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

	leases.CloseAndWait()

	logger.DefaultLogger.Infof("pull_x - leases.WaitAllFinished() - DONE")
	serviceObject.ServiceRoutineRun(func(obj *runningVirtualFolderSyncthingService, done func(), ctx context.Context) {
		defer done()
		if scanner != nil {
			err = scanner.Finish(ctx)
		} else {
			err = puller.Finish(ctx)
		}
		logger.DefaultLogger.Infof("pull_x - scanner.Finish() - DONE, result: %v", err)
		if err != nil {
			logger.DefaultLogger.Warnf("pull_x - scanner.Finish() - ERR: %v", err)
		}
	})

	if isAbortOrErr {
		return nil
	}

	if scanner != nil {
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

func (vf *virtualFolderSyncthingService) UpdateOneLocalFileInfoLocalChangeDetected(fi protocol.FileInfo) {
	err := vf.blobFs.UpdateFile(vf.ctx, fi, func(block protocol.BlockInfo, status GetBlockDataResult) {},
		func(block protocol.BlockInfo) ([]byte, error) {
			panic("UpdateOneLocalFileInfoLocalChangeDetected(): download callback should not be called for already processed local block changes")
		})
	if err != nil {
		logger.DefaultLogger.Warnf("VFolder: UpdateOneLocalFileInfoLocalChangeDetected(): failed to update file. Err: %+v", err)
	}
	vf.updateOneLocalFileInfo(&fi, events.LocalChangeDetected)
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
