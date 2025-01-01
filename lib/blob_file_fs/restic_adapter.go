// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package blobfilefs

import (
	"context"
	"crypto/sha256"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	archiver "github.com/restic/restic/lib/archiver"
	restic_model "github.com/restic/restic/lib/model"
	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/db"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/model"
	"github.com/syncthing/syncthing/lib/protocol"
)

type ResticAdapterBase struct {
	hostname string
	folderID string
	options  archiver.EasyArchiverOptions
}

type ResticAdapter struct {
	*ResticAdapterBase
	ctx       context.Context
	cancelCtx context.CancelFunc

	reader *atomic.Pointer[archiver.EasyArchiveReader]
}

// ForceDropDataBlock implements model.BlobFsI.
func (r *ResticAdapter) ForceDropDataBlock(hash []byte) {
	// TODO: implement
}

func FactoryResticAdapter(
	ctx context.Context,
	ownDeviceID string,
	folderID string,
	path string,
	evLogger events.Logger,
	fset *db.FileSet,
) model.BlobFsI {
	parts := strings.Split(path, ":s3-region:")
	region := "us-east-1"
	url := parts[0]
	if len(parts) > 1 {
		region = parts[1]
	}
	opts := archiver.GetDefaultEasyArchiverOptions()
	opts.Repo = url
	opts.SetPassword("syncthing")
	opts.Options = []string{"s3.region=" + region}

	instance, err := NewResticAdapter(ownDeviceID, folderID, opts)
	if err != nil {
		log.Panicf("Failed to create ResticAdapter: %v", err)
	}
	return instance
}

func NewResticAdapter(hostname string, folderID string, options archiver.EasyArchiverOptions) (*ResticAdapter, error) {
	base := &ResticAdapterBase{
		options:  options,
		hostname: hostname,
		folderID: folderID,
	}

	ctx, cancel := context.WithCancel(context.Background())
	reader, err := archiver.NewEasyArchiveReader(ctx, options)
	if errors.Is(err, archiver.ErrNoRepository) {
		initOpts := archiver.EasyArchiverInitOptions{}
		err = archiver.InitNewRepository(ctx, initOpts, options)
		if err != nil {
			cancel()
			return nil, err
		}

		reader, err = archiver.NewEasyArchiveReader(ctx, options)
	}
	if err != nil {
		cancel()
		return nil, err
	}
	ptr := &atomic.Pointer[archiver.EasyArchiveReader]{}
	ptr.Store(reader)
	return &ResticAdapter{
		ResticAdapterBase: base,
		ctx:               ctx,
		cancelCtx:         cancel,
		reader:            ptr,
	}, nil
}

func (r *ResticAdapter) Close() {
	r.cancelCtx()
	r.replaceReader(nil) // waits internally
}

func (r *ResticAdapter) replaceReader(newPtr *archiver.EasyArchiveReader) {
	readerPtr := r.reader.Swap(newPtr)
	if readerPtr != nil {
		readerPtr.Close()
	}
}

type ResticScannerOrPuller struct {
	parent     *ResticAdapterBase
	scanOpts   model.PullOptions
	serviceCtx context.Context

	snw          *archiver.EasyArchiveWriter
	snapshotDone context.CancelFunc
	done         func()
}

func (r *ResticAdapterBase) getTargets() []string {
	return []string{r.folderID}
}

// StartScanOrPull implements BlobFsI.
func (r *ResticAdapter) StartScanOrPull(ctx context.Context, opts model.PullOptions, done func()) (model.BlobFsScanOrPullI, error) {
	upToDate, err := r.StartScanOrPullConcrete(ctx, opts, done)
	if err != nil {
		return nil, err
	}
	return upToDate, nil
}

// StartScanOrPull implements BlobFsI.
func (r *ResticAdapterBase) StartScanOrPullConcrete(serviceCtx context.Context, opts model.PullOptions, done func()) (*ResticScannerOrPuller, error) {

	workCtx, workDone := context.WithCancel(serviceCtx)
	arch, err := archiver.NewEasyArchiveWriter(
		serviceCtx,
		r.hostname,
		r.getTargets(),
		r.options,
		func(ctx context.Context, eaw *archiver.EasyArchiveWriter) error {
			// wait till work is done externally
			<-workCtx.Done()
			log.Println("ResticAdapterBase::StartScanOrPullConcrete(): snapshot done")
			return nil
		},
	)
	if err != nil {
		workDone()
		return nil, err
	}

	return &ResticScannerOrPuller{
		parent:       r,
		scanOpts:     opts,
		serviceCtx:   serviceCtx,
		snw:          arch,
		snapshotDone: workDone,
		done:         done,
	}, nil
}

func (r *ResticScannerOrPuller) ScanOne(workCtx context.Context, fi *protocol.FileInfo, fn model.JobQueueProgressFn) error {
	result := &model.JobResult{}
	defer func() {
		fn(0, result)
	}()

	if r.scanOpts.OnlyCheck {
		result.Err = UpdateFile(workCtx, r.snw, fi, r.parent.folderID,
			func(block protocol.BlockInfo, status model.GetBlockDataResult) {
				fn(int64(block.Size), nil)
			}, func(block protocol.BlockInfo) ([]byte, error) {
				// if this is called, it means that the block data is missing and should be downloaded
				// but we are in scan mode, so no download is desired
				log.Println("ResticScannerOrPuller::ScanOne(): missing block data")
				return nil, model.ErrMissingBlockData
			})
	} else {
		panic("ResticScannerOrPuller::ScanOne(): should not be called for pull!")
	}

	return result.Err
}

func (r *ResticScannerOrPuller) PullOne(
	workCtx context.Context,
	fi *protocol.FileInfo,
	blockStatusCb func(block protocol.BlockInfo, status model.GetBlockDataResult),
	downloadCb func(block protocol.BlockInfo) ([]byte, error),
) error {
	if r.scanOpts.OnlyCheck {
		panic("ResticScannerOrPuller::PullOne(): should not be called for scan!")
	}
	return UpdateFile(workCtx, r.snw, fi, r.parent.folderID, blockStatusCb, downloadCb)
}

// Finish implements BlobFsScanOrPullI.
func (r *ResticScannerOrPuller) Finish(ctx context.Context) error {
	log.Println("ResticScannerOrPuller::Finish()")
	r.snapshotDone()
	log.Println("ResticScannerOrPuller::Finish(): waiting for snapshot to finish")
	r.snw.Close()
	log.Println("ResticScannerOrPuller::Finish(): snapshot finished")
	if r.done != nil {
		r.done()
	}
	return nil
}

// ReserveAndSetI implements BlobFsI.
func (r *ResticAdapter) ReserveAndSetI(hash []byte, data []byte) {
	panic("unimplemented")
}

// GetHashBlockData implements BlobFsI.
func (r *ResticAdapter) GetHashBlockData(ctx context.Context, hash []byte, response_data []byte) (int, error) {
	readerPtr := r.reader.Load()
	if readerPtr == nil {
		return 0, model.ErrConnectionFailed
	}
	data, err := readerPtr.LoadDataBlob(ctx, restic_model.ID(hash))
	if err != nil {
		return 0, err
	}
	n := copy(response_data, data)
	return n, nil
}

// convertToResticIDs converts []protocol.BlockInfo to restic.IDs
func convertToResticIDs(blocks []protocol.BlockInfo) restic_model.IDs {
	ids := make(restic_model.IDs, len(blocks))
	for i, block := range blocks {
		ids[i] = restic_model.ID(block.Hash)
	}
	return ids
}

var _ model.BlobFsI = &ResticAdapter{}

func MapFileInfoTypeToResticNodeType(t protocol.FileInfoType) restic_model.NodeType {
	switch t {
	case protocol.FileInfoTypeFile:
		return restic_model.NodeTypeFile
	case protocol.FileInfoTypeDirectory:
		return restic_model.NodeTypeDir
	case protocol.FileInfoTypeSymlink:
		return restic_model.NodeTypeSymlink
	default:
		return restic_model.NodeTypeInvalid
	}
}

func GenerateFullName(folderID string, name string) string {
	fullName := "/" + folderID
	if strings.HasPrefix(name, "/") {
		fullName += name
	} else {
		fullName += "/" + name
	}
	return fullName
}

func ConvertFileInfoToResticNode(fi *protocol.FileInfo, folderID string) *restic_model.Node {
	fullName := GenerateFullName(folderID, fi.Name)
	path, name := filepath.Split(fullName)
	return &restic_model.Node{
		Name:               name,
		Type:               MapFileInfoTypeToResticNodeType(fi.Type),
		Mode:               os.FileMode(fi.Permissions),
		ModTime:            fi.ModTime(),
		AccessTime:         fi.ModTime(),
		ChangeTime:         fi.InodeChangeTime(),
		UID:                uint32(fi.Platform.GetUnixUidOrDefault(0)),
		GID:                uint32(fi.Platform.GetUnixGidOrDefault(0)),
		User:               fi.Platform.GetUnixOwnerNameOrDefault(""),
		Group:              fi.Platform.GetUnixGroupNameOrDefault(""),
		DeviceID:           0,
		Size:               uint64(fi.Size),
		Links:              0,
		LinkTarget:         "",
		LinkTargetRaw:      nil,
		ExtendedAttributes: nil,
		GenericAttributes:  nil,
		Device:             0,
		Content:            convertToResticIDs(fi.Blocks),
		Subtree:            nil,
		Error:              "",
		Path:               path,
	}
}

func convertBlockStatusFromRestic(status archiver.BlockUpdateStatus) model.GetBlockDataResult {
	switch status {
	case archiver.BlockUpdateStatusDownloaded:
		return model.GET_BLOCK_DOWNLOAD
	case archiver.BlockUpdateStatusCached:
		return model.GET_BLOCK_CACHED
	case archiver.BlockUpdateStatusError:
		return model.GET_BLOCK_FAILED
	default:
		return model.GET_BLOCK_FAILED
	}
}

func UpdateFile(
	ctx context.Context,
	snw *archiver.EasyArchiveWriter,
	fi *protocol.FileInfo,
	folderID string,
	blockStatusCb func(block protocol.BlockInfo, status model.GetBlockDataResult),
	downloadBlockDataCb func(block protocol.BlockInfo) ([]byte, error),
) error {
	node := ConvertFileInfoToResticNode(fi, folderID)
	return snw.UpdateFile(
		ctx,
		node,
		uint64(fi.BlockSize()),
		func(offset, blockSize uint64, status archiver.BlockUpdateStatus) {
			blockStatusCb(protocol.BlockInfo{
				Offset: int64(offset),
				Size:   int(blockSize),
			}, convertBlockStatusFromRestic(status))
		},
		func(blockIdx uint64, hash []byte) ([]byte, error) {
			return downloadBlockDataCb(fi.Blocks[blockIdx])
		},
	)
}

// UpdateFile implements model.BlobFsI.
func (r *ResticAdapter) UpdateFile(
	ctx context.Context,
	fi *protocol.FileInfo,
	blockStatusCb func(block protocol.BlockInfo, status model.GetBlockDataResult),
	downloadBlockDataCb func(block protocol.BlockInfo) ([]byte, error),
) error {
	tmpSnw, err := r.StartScanOrPullConcrete(ctx, model.PullOptions{OnlyMissing: false, OnlyCheck: false}, nil)
	if err != nil {
		return err
	}
	defer tmpSnw.Finish(ctx)
	return UpdateFile(ctx, tmpSnw.snw, fi, r.folderID, blockStatusCb, downloadBlockDataCb)
}

// ReadFile implements model.BlobFsI.
func (r *ResticAdapter) ReadFileData(ctx context.Context, name string) ([]byte, error) {
	readerPtr := r.reader.Load()
	if readerPtr == nil {
		return nil, model.ErrConnectionFailed
	}

	return readerPtr.ReadFile(
		context.Background(),
		[]string{r.ResticAdapterBase.hostname},
		archiver.TagLists{},
		r.getTargets(),
		"latest",
		GenerateFullName(r.folderID, name),
	)
}

// GetEncryptionToken implements model.BlobFsI.
func (r *ResticAdapter) GetEncryptionToken() ([]byte, error) {
	return r.ReadFileData(r.ctx, "/"+config.EncryptionTokenName)
}

// SetEncryptionToken implements model.BlobFsI.
func (r *ResticAdapter) SetEncryptionToken(data []byte) error {

	dataHash := sha256.Sum256(data)
	dataHashList := restic_model.IDs{restic_model.ID(dataHash[:])}

	node := &restic_model.Node{
		Name:               config.EncryptionTokenName,
		Type:               restic_model.NodeTypeFile,
		Mode:               0777,
		ModTime:            time.Now(),
		AccessTime:         time.Now(),
		ChangeTime:         time.Now(),
		UID:                0,
		GID:                0,
		User:               "root",
		Group:              "root",
		Inode:              0,
		DeviceID:           0,
		Size:               uint64(len(data)),
		Links:              0,
		LinkTarget:         "",
		LinkTargetRaw:      nil,
		ExtendedAttributes: nil,
		GenericAttributes:  nil,
		Device:             0,
		Content:            dataHashList,
		Subtree:            nil,
		Error:              "",
		Path:               "/" + r.folderID,
	}

	writer, err := archiver.NewEasyArchiveWriter(
		context.Background(),
		r.hostname,
		r.getTargets(),
		r.options,
		func(ctx context.Context, eaw *archiver.EasyArchiveWriter) error {
			return eaw.UpdateFile(
				ctx,
				node,
				uint64(len(data)),
				func(offset, blockSize uint64, status archiver.BlockUpdateStatus) {},
				func(blockIdx uint64, hash []byte) ([]byte, error) {
					// will be called only once
					return data, nil
				},
			)
		},
	)
	if err != nil {
		return err
	}

	writer.Close()
	return nil
}
