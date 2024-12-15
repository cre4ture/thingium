// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

// Package virtualmount implements the `syncthing virtualmount` subcommand.
package virtualmount

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/syncthing/syncthing/lib/blockstorage"
	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/db"
	"github.com/syncthing/syncthing/lib/logger"
	"github.com/syncthing/syncthing/lib/model"
	"github.com/syncthing/syncthing/lib/protocol"
	"gocloud.dev/blob"
)

type CLI struct {
	DeviceID  string `help:"Device ID of the virtual folder, if it cannot be determined automatically"`
	FolderID  string `help:"Folder ID of the virtual folder, if it cannot be determined automatically"`
	URL       string `arg:"" required:"1" help:"URL to virtual folder. Excluding \":virtual:\""`
	MountPath string `xor:"mode" placeholder:"PATH" help:"Directory where to mount the virtual folder"`
}

func (c *CLI) Run() error {

	blockStorage := blockstorage.NewGoCloudUrlStorage(context.Background(), c.URL, "")

	devices, err := listDeviceIDs(blockStorage)
	if err != nil {
		return err
	}

	println("known devices:")
	for _, device := range devices {
		println(device)
	}

	if len(devices) == 0 {
		return errors.New("This URL doesn't list any syncthing devices. Abort.")
	}

	if c.DeviceID == "" {
		// by default, take the only device
		if len(devices) > 1 {
			return errors.New("This URL lists multiple syncthing devices. You need to specify which one. Abort.")
		}
		c.DeviceID = devices[0]
	}

	folders, err := listFolderIDs(blockStorage, c.DeviceID)
	if err != nil {
		return err
	}

	println("known folders for device " + c.DeviceID + ":")
	for _, folder := range folders {
		println(folder)
	}

	if len(folders) == 0 {
		return errors.New("This URL doesn't list any syncthing folders for specified device. Abort.")
	}

	if c.FolderID == "" {
		// by default, take the only folder
		if len(folders) > 1 {
			return errors.New("This URL lists multiple syncthing folders for specified device. You need to specify which one. Abort.")
		}
		c.FolderID = folders[0]
	}

	folderLabel := "offline-folder-mount"

	metaPrefix := blockstorage.LOCAL_HAVE_FI_META_PREFIX + "/" +
		c.DeviceID + "/" +
		c.FolderID + "/"

	fsetRO := NewOfflineDbFileSetRead(metaPrefix, blockStorage)

	fsetRW := &OfflineDbFileSetWrite{}
	dataAccess := &OfflineBlockDataAccess{blockStorage: blockStorage}

	stVF := model.NewSyncthingVirtualFolderFuseAdapter(
		protocol.ShortID(0),
		c.FolderID,
		config.FolderTypeReceiveOnly, // for read only
		fsetRO,
		fsetRW,
		dataAccess,
	)

	mount, err := model.NewVirtualFolderMount(
		c.MountPath, c.FolderID, folderLabel, stVF,
	)

	if err != nil {
		return err
	}

	defer mount.Close()

	// block till signal
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt)
	sig := <-signalChan
	log.Printf("Received signal %s; shutting down", sig)

	return nil
}

func listDeviceIDs(storage *blockstorage.GoCloudUrlStorage) ([]string, error) {
	prefix := blockstorage.MetaDataSubFolder + "/" +
		blockstorage.LOCAL_HAVE_FI_META_PREFIX + "/"
	return listSubdirs(storage, prefix, "/")
}

func listFolderIDs(storage *blockstorage.GoCloudUrlStorage, deviceID string) ([]string, error) {
	prefix := blockstorage.MetaDataSubFolder + "/" +
		blockstorage.LOCAL_HAVE_FI_META_PREFIX + "/" +
		deviceID + "/"
	return listSubdirs(storage, prefix, "/")
}

func iterateSubdirs(storage *blockstorage.GoCloudUrlStorage, prefix string, delimiter string, fn func(e *blob.ListObject)) error {
	bucket := storage.RawAccess()
	listCtx, listCtxCancel := context.WithCancel(context.Background())
	defer listCtxCancel()
	opts := &blob.ListOptions{Prefix: prefix, Delimiter: delimiter}
	token := blob.FirstPageToken
	for {
		var page []*blob.ListObject
		var err error
		page, token, err = bucket.ListPage(listCtx, token, 100, opts)
		if err != nil {
			return err
		}

		if len(page) == 0 {
			return nil
		}

		for _, e := range page {
			fn(e)
		}
	}
}

func listSubdirs(storage *blockstorage.GoCloudUrlStorage, prefix string, delimiter string) ([]string, error) {
	names := make([]string, 0)
	iterateSubdirs(storage, prefix, delimiter, func(e *blob.ListObject) {
		name, _ := strings.CutPrefix(e.Key, prefix)
		if delimiter != "" {
			name, _ = strings.CutSuffix(name, delimiter)
		}
		names = append(names, name)
	})
	return names, nil
}

type OfflineBlockDataAccess struct {
	blockStorage *blockstorage.GoCloudUrlStorage
}

// GetBlockDataFromCacheOrDownloadI implements model.BlockDataAccessI.
func (o *OfflineBlockDataAccess) GetBlockDataFromCacheOrDownloadI(
	file *protocol.FileInfo, block protocol.BlockInfo,
) ([]byte, bool, model.GetBlockDataResult) {
	data, ok := o.blockStorage.ReserveAndGet(block.Hash, true)
	return data, ok, model.GET_BLOCK_CACHED
}

// RequestBackgroundDownloadI implements model.BlockDataAccessI.
func (o *OfflineBlockDataAccess) RequestBackgroundDownloadI(filename string, size int64, modified time.Time) {
	// ignore
}

// ReserveAndSetI implements model.BlockDataAccessI.
func (o *OfflineBlockDataAccess) ReserveAndSetI(hash []byte, data []byte) {
	panic("OfflineBlockDataAccess::ReserveAndSetI(): should not be called for read only folder")
}

type OfflineDbFileSetWrite struct {
}

// Update implements model.DbFileSetWriteI.
func (o *OfflineDbFileSetWrite) Update(fs []protocol.FileInfo) {
	panic("OfflineDbFileSetWrite::Update(): should not be called for read only folder")
}

// UpdateOneLocalFileInfoLocalChangeDetected implements model.DbFileSetWriteI.
func (o *OfflineDbFileSetWrite) UpdateOneLocalFileInfoLocalChangeDetected(fi *protocol.FileInfo) {
	panic("OfflineDbFileSetWrite::UpdateOneLocalFileInfoLocalChangeDetected(): should not be called for read only folder")
}

type OfflineDbFileSetRead struct {
	metaPrefix   string
	blockStorage *blockstorage.GoCloudUrlStorage
	cache        map[string]*protocol.FileInfo
}

func NewOfflineDbFileSetRead(
	metaPrefix string,
	blockStorage *blockstorage.GoCloudUrlStorage,
) *OfflineDbFileSetRead {
	return &OfflineDbFileSetRead{
		metaPrefix:   metaPrefix,
		blockStorage: blockStorage,
		cache:        make(map[string]*protocol.FileInfo),
	}
}

// SnapshotI implements model.DbFileSetReadI.
func (o *OfflineDbFileSetRead) SnapshotI() (db.DbSnapshotI, error) {
	return &OfflineDbSnapshotI{o.metaPrefix, o.blockStorage, &o.cache}, nil
}

type OfflineDbSnapshotI struct {
	metaPrefix   string
	blockStorage *blockstorage.GoCloudUrlStorage
	cache        *map[string]*protocol.FileInfo
}

// GetGlobal implements db.DbSnapshotI.
func (o *OfflineDbSnapshotI) GetGlobal(file string) (protocol.FileInfo, bool) {
	fi, ok := (*o.cache)[file]
	logger.DefaultLogger.Debugf("GetGlobal(%v): cache-ok:%v, data len:%v", file, ok, fi)
	if ok {
		return *fi, true
	}

	fi = &protocol.FileInfo{}

	fullUrl := o.metaPrefix + file
	data, ok := o.blockStorage.GetMeta(fullUrl)
	logger.DefaultLogger.Debugf("GetGlobal(%v): %v, ok:%v, data len:%v", file, fullUrl, ok, len(data))
	if !ok {
		return *fi, false
	}

	err := fi.Unmarshal(data)
	logger.DefaultLogger.Debugf("GetGlobal(%v): unmarshal-err: %+v. fi: %+v", file, err, fi)
	if err != nil {
		return *fi, false
	}

	(*o.cache)[file] = fi

	return *fi, true
}

// GetGlobalTruncated implements db.DbSnapshotI.
func (o *OfflineDbSnapshotI) GetGlobalTruncated(file string) (db.FileInfoTruncated, bool) {
	fi, ok := o.GetGlobal(file)
	out := db.FileInfoTruncated{
		Name:          fi.Name,
		Size:          fi.Size,
		ModifiedS:     fi.ModifiedS,
		ModifiedBy:    fi.ModifiedBy,
		Version:       fi.Version,
		Sequence:      fi.Sequence,
		SymlinkTarget: fi.SymlinkTarget,
		BlocksHash:    fi.BlocksHash,
		Encrypted:     fi.Encrypted,
		Type:          fi.Type,
		Permissions:   fi.Permissions,
		ModifiedNs:    fi.ModifiedNs,
		RawBlockSize:  fi.RawBlockSize,
		Platform:      fi.Platform,
		LocalFlags:    fi.LocalFlags,
		VersionHash:   fi.VersionHash,
		InodeChangeNs: fi.InodeChangeNs,
		Deleted:       fi.Deleted,
		RawInvalid:    fi.RawInvalid,
		NoPermissions: fi.NoPermissions,
	}
	return out, ok
}

// Release implements db.DbSnapshotI.
func (o *OfflineDbSnapshotI) Release() {
	// ignore
}

// WithPrefixedGlobalTruncated implements db.DbSnapshotI.
func (o *OfflineDbSnapshotI) WithPrefixedGlobalTruncated(prefix string, fn db.Iterator) {
	logger.DefaultLogger.Debugf("WithPrefixedGlobalTruncated(%v)", prefix)
	rootPrefix := blockstorage.MetaDataSubFolder + "/" + o.metaPrefix
	fullPrefix := rootPrefix + prefix
	if !strings.HasSuffix(fullPrefix, "/") {
		fullPrefix = fullPrefix + "/"
	}
	iterateSubdirs(o.blockStorage, fullPrefix, "/", func(e *blob.ListObject) {
		name, _ := strings.CutPrefix(e.Key, rootPrefix)
		logger.DefaultLogger.Debugf("WithPrefixedGlobalTruncated(%v): %v", prefix, name)
		fi, ok := o.GetGlobal(name)
		logger.DefaultLogger.Debugf("WithPrefixedGlobalTruncated(%v): %v, ok:%v: %+v", prefix, ok, fi)
		if !ok {
			return
		}
		fi.Name, _ = strings.CutPrefix(fi.Name, prefix)
		fn(fi)
	})
}
