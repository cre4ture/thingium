// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package blobfilefs

import (
	"context"
	"crypto/sha256"
	"os"
	"sync/atomic"
	"time"

	archiver "github.com/restic/restic/lib/archiver"
	restic_model "github.com/restic/restic/lib/model"
	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/model"
	"github.com/syncthing/syncthing/lib/protocol"
)

type ResticAdapterBase struct {
	hostname string
	options  archiver.EasyArchiverOptions
}

type ResticAdapter struct {
	*ResticAdapterBase
	ctx       context.Context
	cancelCtx context.CancelFunc

	reader *atomic.Pointer[archiver.EasyArchiveReader]
	puller *atomic.Pointer[ResticScannerOrPuller]
}

func NewResticAdapter(options archiver.EasyArchiverOptions) (*ResticAdapter, error) {
	base := &ResticAdapterBase{
		options: options,
	}
	ctx, cancel := context.WithCancel(context.Background())
	reader, err := archiver.NewEasyArchiveReader(ctx, options)
	if err != nil {
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
	parent   *ResticAdapterBase
	scanOpts model.PullOptions
	ctx      context.Context

	snw    *archiver.EasyArchiveWriter
	cancel context.CancelFunc
}

// StartScanOrPull implements BlobFsI.
func (r *ResticAdapter) StartScanOrPull(ctx context.Context, opts model.PullOptions) (model.BlobFsScanOrPullI, error) {
	upToDate, err := r.StartScanOrPullConcrete(ctx, opts)
	if err != nil {
		return nil, err
	}
	return upToDate, nil
}

// StartScanOrPull implements BlobFsI.
func (r *ResticAdapterBase) StartScanOrPullConcrete(ctx context.Context, opts model.PullOptions) (*ResticScannerOrPuller, error) {

	snapshotCtx, cancel := context.WithCancel(ctx)
	arch, err := archiver.NewEasyArchiveWriter(
		ctx,
		r.options,
		func(ctx context.Context, eaw *archiver.EasyArchiveWriter) error {
			// wait till work is done externally
			<-snapshotCtx.Done()
			return nil
		},
	)
	if err != nil {
		cancel()
		return nil, err
	}

	return &ResticScannerOrPuller{
		parent:   r,
		scanOpts: opts,
		ctx:      ctx,
		snw:      arch,
		cancel:   cancel,
	}, nil
}

// DoOne implements BlobFsScanOrPullI.
func (r *ResticScannerOrPuller) DoOne(fi *protocol.FileInfo, fn model.JobQueueProgressFn) error {
	if r.scanOpts.OnlyCheck {
		return UpdateFile(r.ctx, r.snw, fi, func(block protocol.BlockInfo, status model.GetBlockDataResult) {
			// noop
		}, func(block protocol.BlockInfo) ([]byte, error) {
			// if this is called, it means that the block data is missing and should be downloaded
			// but we are in scan mode, so no download is desired
			return nil, model.ErrMissingBlockData
		})
	} else {
		panic("ResticScannerOrPuller::DoOne(): should not be called for pull!")
	}
}

// Finish implements BlobFsScanOrPullI.
func (r *ResticScannerOrPuller) Finish() error {
	r.cancel()
	r.snw.Close()
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

func ConvertFileInfoToResticNode(fi *protocol.FileInfo) *restic_model.Node {
	return &restic_model.Node{
		Name:               fi.FileName(),
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
		Path:               fi.Name,
	}
}

func UpdateFile(
	ctx context.Context,
	snw *archiver.EasyArchiveWriter,
	fi *protocol.FileInfo,
	blockStatusCb func(block protocol.BlockInfo, status model.GetBlockDataResult),
	downloadBlockDataCb func(block protocol.BlockInfo) ([]byte, error),
) error {
	node := ConvertFileInfoToResticNode(fi)
	return snw.UpdateFile(
		ctx,
		fi.Name,
		node,
		uint64(fi.BlockSize()),
		node.Content,
		func(blockIdx uint64, hash []byte) ([]byte, error) {
			return downloadBlockDataCb(fi.Blocks[blockIdx])
		},
	)
}

// UpdateFile implements model.BlobFsI.
func (r *ResticAdapter) UpdateFile(ctx context.Context, fi *protocol.FileInfo, blockStatusCb func(block protocol.BlockInfo, status model.GetBlockDataResult), downloadBlockDataCb func(block protocol.BlockInfo) ([]byte, error)) error {
	tmpSnw, err := r.StartScanOrPullConcrete(ctx, model.PullOptions{OnlyMissing: false, OnlyCheck: false})
	if err != nil {
		return err
	}
	defer tmpSnw.Finish()
	return UpdateFile(ctx, tmpSnw.snw, fi, blockStatusCb, downloadBlockDataCb)
}

// GetEncryptionToken implements model.BlobFsI.
func (r *ResticAdapter) GetEncryptionToken() (data []byte, err error) {
	readerPtr := r.reader.Load()
	if readerPtr == nil {
		return nil, model.ErrConnectionFailed
	}

	data = nil
	readerPtr.ReadFile(
		context.Background(),
		[]string{r.ResticAdapterBase.hostname},
		archiver.TagLists{},
		[]string{},
		"latest",
		config.EncryptionTokenName,
	)
	return data, nil
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
		Path:               "/",
	}

	writer, err := archiver.NewEasyArchiveWriter(
		context.Background(),
		r.options,
		func(ctx context.Context, eaw *archiver.EasyArchiveWriter) error {
			eaw.UpdateFile(
				ctx,
				config.EncryptionTokenName,
				node,
				uint64(len(data)),
				dataHashList,
				func(blockIdx uint64, hash []byte) ([]byte, error) {
					// will be called only once
					return data, nil
				},
			)
			return nil
		},
	)
	if err != nil {
		return err
	}

	writer.Close()
	return nil
}
