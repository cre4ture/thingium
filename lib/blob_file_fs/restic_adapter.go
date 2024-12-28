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
	snw archiver.EasyArchiver
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
