// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package virtual

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/syncthing/syncthing/internal/gen/bep"
	"github.com/syncthing/syncthing/lib/blockstorage"
	"github.com/syncthing/syncthing/lib/db"
	"github.com/syncthing/syncthing/lib/logger"
	"github.com/syncthing/syncthing/lib/model"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/utils"
	"google.golang.org/protobuf/proto"
)

type OfflineStorageAccess struct {
	BlockStorage model.HashBlockStorageI
	DeviceID     string
	FolderID     string
	FSetRO       *OfflineDbFileSetRead
}

func NewOfflineDataAccess(URL string, DeviceID string, FolderID string) (*OfflineStorageAccess, error) {
	BlockStorage := blockstorage.NewGoCloudUrlStorageFromConfigStr(context.Background(), URL, "")

	devices, err := listDeviceIDs(BlockStorage)
	if err != nil {
		return nil, err
	}

	println("known devices:")
	for _, device := range devices {
		println(device)
	}

	if len(devices) == 0 {
		return nil, errors.New("this URL doesn't list any syncthing devices. Abort")
	}

	if DeviceID == "" {
		// by default, take the only device
		if len(devices) > 1 {
			return nil, errors.New("this URL lists multiple syncthing devices. You need to specify which one. Abort")
		}
		DeviceID = devices[0]
	}

	folders, err := listFolderIDs(BlockStorage, DeviceID)
	if err != nil {
		return nil, err
	}

	println("known folders for device " + DeviceID + ":")
	for _, folder := range folders {
		println(folder)
	}

	if len(folders) == 0 {
		return nil, errors.New("this URL doesn't list any syncthing folders for specified device. Abort")
	}

	if FolderID == "" {
		// by default, take the only folder
		if len(folders) > 1 {
			return nil, errors.New("this URL lists multiple syncthing folders for specified device. You need to specify which one. Abort")
		}
		FolderID = folders[0]
	}

	metaPrefix := blockstorage.LOCAL_HAVE_FI_META_PREFIX + "/" +
		DeviceID + "/" +
		FolderID + "/"

	fsetRO := NewOfflineDbFileSetRead(metaPrefix, BlockStorage)

	return &OfflineStorageAccess{
		BlockStorage: BlockStorage,
		DeviceID:     DeviceID,
		FolderID:     FolderID,
		FSetRO:       fsetRO,
	}, nil
}

func listDeviceIDs(storage model.HashBlockStorageI) ([]string, error) {
	prefix := blockstorage.MetaDataSubFolder + "/" +
		blockstorage.LOCAL_HAVE_FI_META_PREFIX + "/"
	return listSubdirs(storage, prefix, "/")
}

func listFolderIDs(storage model.HashBlockStorageI, deviceID string) ([]string, error) {
	prefix := blockstorage.MetaDataSubFolder + "/" +
		blockstorage.LOCAL_HAVE_FI_META_PREFIX + "/" +
		deviceID + "/"
	return listSubdirs(storage, prefix, "/")
}

func listSubdirs(storage model.HashBlockStorageI, prefix string, delimiter string) ([]string, error) {
	names := make([]string, 0)
	storage.IterateSubdirs(prefix, delimiter, func(e *model.ListObject) {
		name, _ := strings.CutPrefix(string(e.Key), prefix)
		if delimiter != "" {
			name, _ = strings.CutSuffix(name, delimiter)
		}
		names = append(names, name)
	})
	return names, nil
}

type Caches struct {
	fileCache *utils.Protected[map[string]*protocol.FileInfo]
	dirCache  *utils.Protected[map[string]*utils.Protected[[]*protocol.FileInfo]]
}

type OfflineDbSnapshotI struct {
	metaPrefix   string
	blockStorage model.HashBlockStorageI
	caches       *Caches
}

// GetGlobal implements db.DbSnapshotI.
func (o *OfflineDbSnapshotI) GetGlobal(file string) (protocol.FileInfo, bool) {
	var fi *protocol.FileInfo = nil
	var ok bool = false
	func() {
		cache := o.caches.fileCache.Lock()
		defer o.caches.fileCache.Unlock()
		fi, ok = (*cache)[file]
	}()

	logger.DefaultLogger.Debugf("GetGlobal(%v): cache-ok:%v, data len:%v", file, ok, fi)
	if ok {
		if fi == nil {
			return protocol.FileInfo{}, false
		}
		return *fi, true
	}

	fullUrl := o.metaPrefix + file
	data, err := o.blockStorage.GetMeta(fullUrl)
	logger.DefaultLogger.Debugf("GetGlobal(%v): %v, ok:%v, data len:%v", file, fullUrl, ok, len(data))
	if err != nil {
		func() {
			cache := o.caches.fileCache.Lock()
			defer o.caches.fileCache.Unlock()
			(*cache)[file] = nil
		}()
		return protocol.FileInfo{}, false
	}

	wireFi := &bep.FileInfo{}
	err = proto.Unmarshal(data, wireFi)
	fiCpy := protocol.FileInfoFromWire(wireFi)
	fi = &fiCpy
	logger.DefaultLogger.Debugf("GetGlobal(%v): unmarshal-err: %+v. fi: %+v", file, err, fi)
	if err != nil {
		return protocol.FileInfo{}, false
	}

	func() {
		cache := o.caches.fileCache.Lock()
		defer o.caches.fileCache.Unlock()
		(*cache)[file] = fi
	}()

	return *fi, true
}

// GetGlobalTruncated implements db.DbSnapshotI.
func (o *OfflineDbSnapshotI) GetGlobalTruncated(file string) (protocol.FileInfo, bool) {
	return o.GetGlobal(file)
}

// Release implements db.DbSnapshotI.
func (o *OfflineDbSnapshotI) Release() {
	// ignore
}

// WithPrefixedGlobalTruncated implements db.DbSnapshotI.
func (o *OfflineDbSnapshotI) WithPrefixedGlobalTruncated(prefix string, fn db.Iterator) {
	logger.DefaultLogger.Debugf("WithPrefixedGlobalTruncated(%v)", prefix)
	rootPrefix := blockstorage.MetaDataSubFolder + "/" + o.metaPrefix
	if (len(prefix) != 0) && !strings.HasSuffix(prefix, "/") {
		prefix = prefix + "/"
	}

	var pChilds *utils.Protected[[]*protocol.FileInfo] = nil
	var childs *[]*protocol.FileInfo = nil
	ok := false
	func() {
		cache := o.caches.dirCache.Lock()
		defer o.caches.dirCache.Unlock()
		pChilds, ok = (*cache)[prefix]
		if !ok {
			pChilds = utils.NewProtected([]*protocol.FileInfo{})
			(*cache)[prefix] = pChilds
			childs = pChilds.Lock() // prevent anybody else
		}
	}()

	func() {
		if ok {
			childs = pChilds.Lock() // here it doesn't matter
		}
		defer pChilds.Unlock()

		if !ok {
			ch := make(chan *protocol.FileInfo, 10)
			wg := sync.WaitGroup{}

			fullPrefix := rootPrefix + prefix
			o.blockStorage.IterateSubdirs(fullPrefix, "/", func(e *model.ListObject) {
				wg.Add(1)
				go func() {
					defer wg.Done()
					name, _ := strings.CutPrefix(string(e.Key), rootPrefix)
					logger.DefaultLogger.Debugf("WithPrefixedGlobalTruncated(%v): %v", prefix, name)
					fi, ok := o.GetGlobal(name)
					logger.DefaultLogger.Debugf("WithPrefixedGlobalTruncated(%v): %v, ok:%v: %+v", prefix, ok, fi)
					if !ok {
						return
					}
					fi.Name, _ = strings.CutPrefix(fi.Name, prefix)
					ch <- &fi
				}()
			})

			go func() {
				wg.Wait()
				close(ch)
			}()

			for fi := range ch {
				*childs = append(*childs, fi)
			}
		}

		for _, child := range *childs {
			fn(*child)
		}
	}()
}

type OfflineDbFileSetRead struct {
	metaPrefix   string
	blockStorage model.HashBlockStorageI
	caches       *Caches
}

func NewOfflineDbFileSetRead(
	metaPrefix string,
	blockStorage model.HashBlockStorageI,
) *OfflineDbFileSetRead {
	return &OfflineDbFileSetRead{
		metaPrefix:   metaPrefix,
		blockStorage: blockStorage,
		caches: &Caches{
			fileCache: utils.NewProtected[map[string]*protocol.FileInfo](make(map[string]*protocol.FileInfo)),
			dirCache:  utils.NewProtected[map[string]*utils.Protected[[]*protocol.FileInfo]](make(map[string]*utils.Protected[[]*protocol.FileInfo])),
		},
	}
}

// SnapshotI implements model.DbFileSetReadI.
func (o *OfflineDbFileSetRead) SnapshotI(closer utils.Closer) (db.DbSnapshotI, error) {
	snap := &OfflineDbSnapshotI{o.metaPrefix, o.blockStorage, o.caches}
	closer.RegisterCleanupFunc(func() error {
		snap.Release()
		return nil
	})
	return snap, nil
}
