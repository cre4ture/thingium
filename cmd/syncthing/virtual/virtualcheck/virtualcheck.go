// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

// Package virtualmount implements the `syncthing virtualcheck` subcommand.
package virtualcheck

import (
	"crypto/sha256"
	"errors"

	"github.com/syncthing/syncthing/cmd/syncthing/virtual"
	"github.com/syncthing/syncthing/lib/hashutil"
	"github.com/syncthing/syncthing/lib/logger"
	"github.com/syncthing/syncthing/lib/protocol"
)

type CLI struct {
	DeviceID string `help:"Device ID of the virtual folder, if it cannot be determined automatically"`
	FolderID string `help:"Folder ID of the virtual folder, if it cannot be determined automatically"`
	URL      string `arg:"" required:"1" help:"URL to virtual folder. Excluding \":virtual:\""`
}

type BlockStatus int

const (
	BLOCK_STATUS_UNKNOWN BlockStatus = iota
	BLOCK_STATUS_GOOD
	BLOCK_STATUS_NOT_AVAILABLE
	BLOCK_STATUS_CORRUPTED
)

func (c *CLI) Run() error {

	osa, err := virtual.NewOfflineDataAccess(
		c.URL, c.DeviceID, c.FolderID,
	)

	if err != nil {
		return err
	}

	snap, err := osa.FSetRO.SnapshotI()
	if err != nil {
		return err
	}

	neededBlocks := make(map[string]BlockStatus)
	corruptedFiles := make(map[string]string)

	snap.WithPrefixedGlobalTruncated("", func(f protocol.FileInfo) bool {
		for i, bi := range f.Blocks {
			hashKey := hashutil.HashToStringMapKey(bi.Hash)
			status, ok := neededBlocks[hashKey]
			if !ok {
				data, err := osa.BlockStorage.ReserveAndGet(bi.Hash, true)
				if err != nil {
					status = BLOCK_STATUS_NOT_AVAILABLE
				} else {
					currentHash := sha256.Sum256(data)
					currentHashKey := hashutil.HashToStringMapKey(currentHash[:])
					if currentHashKey == hashKey {
						status = BLOCK_STATUS_GOOD
					} else {
						status = BLOCK_STATUS_CORRUPTED
						logger.DefaultLogger.Warnf("validating %v:%v - hash mismatch: %s != %s", f.Name, i, currentHashKey, hashKey)
					}
				}

				neededBlocks[hashKey] = status
			}

			if status != BLOCK_STATUS_GOOD {
				logger.DefaultLogger.Warnf("validating %v:%v - hash mismatch", f.Name, i)
				corruptedFiles[f.Name] = "BAD"
			}
		}

		return true
	})

	if len(corruptedFiles) > 0 {
		return errors.New("Found corrupted files")
	}

	logger.DefaultLogger.Infof("Validation SUCCESS. Blocks scanned: %v", len(neededBlocks))

	return nil
}
