// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package blobfilefs

import (
	"context"
	"os"

	archiver "github.com/restic/restic/lib/archiver"
	restic_model "github.com/restic/restic/lib/model"
	"github.com/syncthing/syncthing/lib/protocol"
)

type ResticAdapter struct {
	snw *archiver.EasyArchiver
}

// ReserveAndSetI implements BlobFsI.
func (r *ResticAdapter) ReserveAndSetI(hash []byte, data []byte) {
	panic("unimplemented")
}

// StartScanOrPull implements BlobFsI.
func (r *ResticAdapter) StartScanOrPull(ctx context.Context, opts PullOptions) (BlobFsScanOrPullI, error) {
	panic("unimplemented")
}

// GetHashBlockData implements BlobFsI.
func (r *ResticAdapter) GetHashBlockData(ctx context.Context, hash []byte, response_data []byte) (int, error) {
	data, err := r.snw.LoadDataBlob(ctx, restic_model.ID(hash))
	if err != nil {
		return 0, err
	}
	n := copy(response_data, data)
	return n, nil
}

// GetMeta implements BlobFsI.
func (r *ResticAdapter) GetMeta(name string) (data []byte, err error) {
	panic("unimplemented")
}

// SetMeta implements BlobFsI.
func (r *ResticAdapter) SetMeta(name string, data []byte) error {
	panic("unimplemented")
}

// StartScan implements BlobFsI.
func (r *ResticAdapter) StartScan(ctx context.Context, opts PullOptions) error {
	panic("unimplemented")
}

// convertToResticIDs converts []protocol.BlockInfo to restic.IDs
func convertToResticIDs(blocks []protocol.BlockInfo) restic_model.IDs {
	ids := make(restic_model.IDs, len(blocks))
	for i, block := range blocks {
		ids[i] = restic_model.ID(block.Hash)
	}
	return ids
}

var _ BlobFsI = &ResticAdapter{}

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

// UpdateFile implements BlobFsI.
func (r *ResticAdapter) UpdateFile(
	ctx context.Context,
	fi *protocol.FileInfo,
	blockStatusCb func(block protocol.BlockInfo, status GetBlockDataResult),
	downloadBlockDataCb func(block protocol.BlockInfo) ([]byte, error),
) error {

	content := convertToResticIDs(fi.Blocks)
	return r.snw.UpdateFile(
		ctx,
		fi.Name,
		&restic_model.Node{
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
			Content:            content,
			Subtree:            nil,
			Error:              "",
			Path:               fi.Name,
		},
		uint64(fi.BlockSize()),
		content,
		func(blockIdx uint64, hash []byte) ([]byte, error) {
			return downloadBlockDataCb(fi.Blocks[blockIdx])
		},
	)
}
