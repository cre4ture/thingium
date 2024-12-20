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
	"fmt"

	progressbar "github.com/schollz/progressbar/v3"
	"github.com/syncthing/syncthing/cmd/syncthing/virtual"
	"github.com/syncthing/syncthing/lib/hashutil"
	"github.com/syncthing/syncthing/lib/logger"
	"github.com/syncthing/syncthing/lib/protocol"
)

type CLI struct {
	DeviceID     string `help:"Device ID of the virtual folder, if it cannot be determined automatically"`
	FolderID     string `help:"Folder ID of the virtual folder, if it cannot be determined automatically"`
	URL          string `arg:"" required:"1" help:"URL to virtual folder. Excluding \":virtual:\""`
	ValidateData bool   `help:"If 1 (default), content of data is fetched and checksum validated" default:"1"`
}

type BlockStatus int

const (
	BLOCK_STATUS_UNKNOWN BlockStatus = iota
	BLOCK_STATUS_GOOD
	BLOCK_STATUS_NOT_AVAILABLE
	BLOCK_STATUS_CORRUPTED
)

func (c *CLI) Run() error {

	if !c.ValidateData {
		println("Validation of data is turned OFF")
	} else {
		println("Validation of data is turned ON")
	}

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
	directoriesTodo := make(map[string]*protocol.FileInfo)

	getFirstTodo := func() (prefix string, info *protocol.FileInfo) {
		for k := range directoriesTodo {
			delete(directoriesTodo, k)
			return
		}

		return "", nil
	}

	iterateDir := func(prefix string) {
		println(fmt.Sprintf("Start iterating prefix: %v", prefix))
		snap.WithPrefixedGlobalTruncated(prefix, func(f protocol.FileInfo) bool {

			if f.IsDirectory() {
				directoriesTodo[prefix+f.Name] = &f
				return true
			}

			println(fmt.Sprintf("Start validation of %v", prefix+f.Name))
			bar := progressbar.New(len(f.Blocks))
			defer func() {
				bar.Clear()
				println("")
			}()

			for i, bi := range f.Blocks {
				hashKey := hashutil.HashToStringMapKey(bi.Hash)
				status, ok := neededBlocks[hashKey]
				if !ok {
					data, err := osa.BlockStorage.ReserveAndGet(bi.Hash, c.ValidateData)
					if err != nil {
						status = BLOCK_STATUS_NOT_AVAILABLE
					} else {
						if c.ValidateData {
							currentHash := sha256.Sum256(data)
							currentHashKey := hashutil.HashToStringMapKey(currentHash[:])
							if currentHashKey == hashKey {
								status = BLOCK_STATUS_GOOD
							} else {
								status = BLOCK_STATUS_CORRUPTED
								logger.DefaultLogger.Warnf("validating %v:%v - hash mismatch: %s != %s", f.Name, i, currentHashKey, hashKey)
							}
						} else {
							status = BLOCK_STATUS_GOOD
						}
					}

					neededBlocks[hashKey] = status
				}

				if status != BLOCK_STATUS_GOOD {
					logger.DefaultLogger.Warnf("validating %v:%v - hash mismatch", f.Name, i)
					corruptedFiles[f.Name] = "BAD"
				}

				bar.Add(1)
			}

			return true
		})
	}

	iterateDir("")

	for {
		prefix, info := getFirstTodo()
		if info == nil {
			// done
			break
		}

		iterateDir(prefix)
	}

	if len(corruptedFiles) > 0 {
		return errors.New("found corrupted files")
	}

	logger.DefaultLogger.Infof("Validation SUCCESS. Blocks scanned: %v", len(neededBlocks))

	return nil
}
