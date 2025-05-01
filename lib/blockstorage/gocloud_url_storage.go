// Copyright (C) 2024 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package blockstorage

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/syncthing/syncthing/lib/hashutil"
	"github.com/syncthing/syncthing/lib/logger"
	"github.com/syncthing/syncthing/lib/model"
	"github.com/syncthing/syncthing/lib/utils"
	"gocloud.dev/blob"

	_ "gocloud.dev/blob/azureblob"
	_ "gocloud.dev/blob/fileblob"
	_ "gocloud.dev/blob/gcsblob"
	_ "gocloud.dev/blob/memblob"
	_ "gocloud.dev/blob/s3blob"
)

const BlockDataSubFolder = "blocks"
const MetaDataSubFolder = "meta"
const BLOCK_DELETE_TAG = "deletion-by"
const BLOCK_USE_TAG = "used-by"

type GoCloudUrlStorage struct {
	// io.Closer
	url                string
	hashStringStrategy *hashutil.HashStringStrategy
	ctx                context.Context
	bucket             *blob.Bucket
	myDeviceId         string
}

func (hm *GoCloudUrlStorage) RawAccess() *blob.Bucket {
	return hm.bucket
}

func (hm *GoCloudUrlStorage) IsReadOnly() bool {
	return hm.myDeviceId == ""
}

func (hm *GoCloudUrlStorage) getATag(hash []byte, tag string) string {
	hashKey := hm.getBlockStringKey(hash)
	return hashKey + "." + tag + "." + hm.myDeviceId
}

func (hm *GoCloudUrlStorage) putATag(hash []byte, tag string, updateTime bool) error {
	if hm.IsReadOnly() {
		return errors.New("putATag: read only")
	}

	hashDeviceTagKey := hm.getATag(hash, tag)
	// force existence of stag with our ID
	existsAlready := false
	err := error(nil)
	if !updateTime {
		existsAlready, err = hm.bucket.Exists(hm.ctx, hashDeviceTagKey)
		if err != nil {
			return err
		}
	}
	//if tag != BLOCK_USE_TAG { // skip logging for use tags as this spams
	//	logger.DefaultLogger.Debugf("Put tag %v (exists: %v): %v", tag, existsAlready, hashDeviceTagKey)
	//}
	if existsAlready {
		return nil
	}
	return hm.bucket.WriteAll(hm.ctx, hashDeviceTagKey, []byte{}, nil)
}

func (hm *GoCloudUrlStorage) removeATag(hash []byte, tag string) error {
	if hm.IsReadOnly() {
		return errors.New("removeATag: read only")
	}

	reservationKey := hm.getBlockStringKey(hash) + "." + tag + "." + hm.myDeviceId
	// logger.DefaultLogger.Debugf("removing tag %v: %v", tag, reservationKey)
	return hm.bucket.Delete(hm.ctx, reservationKey)
}

// AnnounceDelete implements HashBlockStorageI.
func (hm *GoCloudUrlStorage) AnnounceDelete(hash []byte) error {
	return hm.putATag(hash, BLOCK_DELETE_TAG, true)
}

// DeAnnounceDelete implements HashBlockStorageI.
func (hm *GoCloudUrlStorage) DeAnnounceDelete(hash []byte) error {
	return hm.removeATag(hash, BLOCK_DELETE_TAG)
}

// UncheckedDelete implements HashBlockStorageI.
func (hm *GoCloudUrlStorage) UncheckedDelete(hash []byte) error {
	if hm.IsReadOnly() {
		return errors.New("UncheckedDelete: read only")
	}

	stringKey := hm.getBlockStringKey(hash)
	return hm.bucket.Delete(hm.ctx, stringKey)
}

// GetBlockHashesCountHint implements HashBlockStorageI.
func (hm *GoCloudUrlStorage) GetBlockHashesCountHint() (int, error) {

	minimum := 100

	logger.DefaultLogger.Debugf("GetBlockHashesCountHint() - enter")
	data, err := hm.GetMeta("BlockCountHint")
	if err != nil {
		logger.DefaultLogger.Infof("GetBlockHashesCountHint() - read hint failed - use %v", minimum)
		return minimum, err
	}

	hint, err := strconv.Atoi(string(data[:]))
	if err != nil {
		logger.DefaultLogger.Infof("GetBlockHashesCountHint() - parsing of hint failed (%v) - use %v", err, minimum)
		return minimum, err
	}

	if hint < minimum {
		hint = minimum
	}

	logger.DefaultLogger.Infof("GetBlockHashesCountHint() - read hint OK: %v", hint)
	return hint, nil
}

func NewGoCloudUrlStorageFromConfigStrConcrete(ctx context.Context, configStr string, myDeviceId string) *GoCloudUrlStorage {

	virtualDescriptor_dash := ""
	blobUrl, hasVirtualDescriptor_def := strings.CutPrefix(configStr, ":virtual:")
	if !hasVirtualDescriptor_def {
		hasVirtualDescriptor_dash := false
		blobUrl, hasVirtualDescriptor_dash = strings.CutPrefix(configStr, ":virtual-")
		if !hasVirtualDescriptor_dash {
			panic("missing :virtual:, or :virtual-s:")
		}
		descEndPos := strings.Index(blobUrl, ":")
		if descEndPos < 0 {
			panic("wrong format of :virtual-xxx:")
		}
		virtualDescriptor_dash = blobUrl[:descEndPos]
		blobUrl = blobUrl[descEndPos+1:]
		logger.DefaultLogger.Infof("NewGoCloudUrlStorageFromConfigStr(): using slashed hash strings: %v", virtualDescriptor_dash)
	} else {
		logger.DefaultLogger.Infof("NewGoCloudUrlStorageFromConfigStr(): using normal hash strings")
	}

	return NewGoCloudUrlStorage(ctx, blobUrl, myDeviceId, hashutil.NewHashStringStrategy(virtualDescriptor_dash))
}

func NewGoCloudUrlStorage(ctx context.Context, url string, myDeviceId string, hashStringStrategy *hashutil.HashStringStrategy) *GoCloudUrlStorage {
	bucket, err := blob.OpenBucket(context.Background(), url)
	if err != nil {
		log.Fatal(err)
	}

	logger.DefaultLogger.Infof("NewGoCloudUrlStorage(): url:%v, myDeviceId:%v, hashStringStrategy:%+v", url, myDeviceId, hashStringStrategy)

	instance := &GoCloudUrlStorage{
		url:                url,
		ctx:                ctx,
		bucket:             bucket,
		myDeviceId:         myDeviceId,
		hashStringStrategy: hashStringStrategy,
	}

	return instance
}

func (hm *GoCloudUrlStorage) CalculateInternalPathRepresentationFromHash(hash []byte) string {
	return hm.hashStringStrategy.HashToSlashedStringMapKey(hash)
}

func (hm *GoCloudUrlStorage) getBlockStringKey(hash []byte) string {
	return BlockDataSubFolder + "/" + hm.CalculateInternalPathRepresentationFromHash(hash)
}

func getMetadataStringKey(name string) string {
	return MetaDataSubFolder + "/" + name
}

func (hm *GoCloudUrlStorage) GetBlockHashesCache(
	ctx context.Context,
	progressNotifier func(count int, currentHash []byte),
) (model.HashBlockStateMap, error) {

	startTime := time.Now()
	defer func() {
		logger.DefaultLogger.Infof("Total time for cached blocks listing: %v minutes", time.Since(startTime).Minutes())
	}()

	hashSet := make(map[string]model.HashBlockState)
	err := hm.IterateBlocks(ctx, func(d model.HashAndState) {

		hashString := hashutil.HashToStringMapKey(d.Hash)
		hashSet[hashString] = d.State
		// logger.DefaultLogger.Infof("IterateBlocks hash(hash, state): %v, %v", hashString, state)
		progressNotifier(len(hashSet), d.Hash)
	})

	if err != nil {
		logger.DefaultLogger.Warnf("IterateBlocks returned error: %v", err)
		return nil, err
	}

	blockCountHint := strconv.Itoa(len(hashSet))
	_ = hm.SetMeta("BlockCountHint", []byte(blockCountHint))
	speedElementsPerSecond := float64(len(hashSet)) / time.Since(startTime).Seconds()
	logger.DefaultLogger.Debugf("SetMeta(BlockCountHint): %v, speed(block/s): %v", blockCountHint, speedElementsPerSecond)
	return hashSet, nil
}

func (hm *GoCloudUrlStorage) GetBlockHashState(hash []byte) (model.HashBlockState, error) {
	blockState := model.HashBlockState{}
	err := hm.IterateBlocksInternal(hm.ctx, hashutil.HashToStringMapKey(hash), func(d model.HashAndState) {
		blockState = d.State
	})

	return blockState, err
}

func (hm *GoCloudUrlStorage) reserveAndCheckExistence(hash []byte) error {
	hashKey := hm.getBlockStringKey(hash)

	if !hm.IsReadOnly() {
		// force existence of use-tag with our ID
		err := hm.putATag(hash, BLOCK_USE_TAG, false)
		if err != nil {
			return err
		}
	}

	perPageCount := 10 // want to see any "delete" token as well.
	opts := &blob.ListOptions{}
	opts.Prefix = hashKey
	page, _, err := hm.bucket.ListPage(hm.ctx, blob.FirstPageToken, perPageCount, opts)
	if err != nil {
		return err
	}

	usesMap := map[string]*blob.ListObject{}
	deletesMap := map[string]*blob.ListObject{}
	var dataEntry *blob.ListObject = nil
	for _, entry := range page {
		suffix, _ := strings.CutPrefix(entry.Key, opts.Prefix)
		if len(suffix) == 0 {
			dataEntry = entry
		} else if deviceId, ok := strings.CutPrefix(suffix, "."+BLOCK_USE_TAG+"."); ok {
			usesMap[deviceId] = entry
		} else if deviceId, ok := strings.CutPrefix(suffix, "."+BLOCK_DELETE_TAG+"."); ok {
			if time.Since(entry.ModTime) < TIME_CONSTANT_BASE {
				deletesMap[deviceId] = entry
			}
		} else {
			//logger.DefaultLogger.Debugf("Object with unknown suffix(key, tag): %v, %v", entry.Key, suffix)
		}
	}

	if len(deletesMap) > 0 {
		// wait until all deletes are processed completely.
		// This should be very rarely happing, thus a simple retry later should not
		// influence overall performance
		return model.ErrRetryLater
	}

	if dataEntry == nil {
		return model.ErrNotAvailable
	}

	return nil
}

func (hm *GoCloudUrlStorage) ReserveAndGet(hash []byte, access model.AccessType) (data []byte, err error) {
	if len(hash) == 0 {
		return nil, model.ErrNotAvailable
	}

	//logger.DefaultLogger.Infof("ReserveAndGet(): %v", hashutil.HashToStringMapKey(hash))
	//defer logger.DefaultLogger.Infof("ReserveAndGet(): %v", hashutil.HashToStringMapKey(hash))

	for {
		err = hm.reserveAndCheckExistence(hash)
		if !errors.Is(err, model.ErrRetryLater) {
			break
		}
		// wait for a relatively long period of time to allow deletion to complete / skip
		//logger.DefaultLogger.Infof("ReserveAndGet(): %v - WAIT for retry", hashutil.HashToStringMapKey(hash))
		time.Sleep(time.Minute * 1)
	}

	if (err == nil) && (access == model.DOWNLOAD_DATA) {
		var err error = nil
		//logger.DefaultLogger.Infof("ReserveAndGet(): %v - download", hashutil.HashToStringMapKey(hash))
		data, err = hm.bucket.ReadAll(hm.ctx, hm.getBlockStringKey(hash))
		if err != nil {
			return nil, model.ErrConnectionFailed
		}
	}

	return data, err
}

func (hm *GoCloudUrlStorage) UncheckedGet(hash []byte, access model.AccessType) (data []byte, err error) {
	if len(hash) == 0 {
		return nil, model.ErrNotAvailable
	}

	//logger.DefaultLogger.Infof("ReserveAndGet(): %v", hashutil.HashToStringMapKey(hash))
	//defer logger.DefaultLogger.Infof("ReserveAndGet(): %v", hashutil.HashToStringMapKey(hash))

	key := hm.getBlockStringKey(hash)
	if access == model.DOWNLOAD_DATA {
		var err error = nil
		//logger.DefaultLogger.Infof("ReserveAndGet(): %v - download", hashutil.HashToStringMapKey(hash))
		data, err = hm.bucket.ReadAll(hm.ctx, key)
		if err != nil {
			return nil, model.ErrConnectionFailed
		}
	} else {
		exists, err := hm.bucket.Exists(hm.ctx, key)
		if err != nil {
			return nil, model.ErrConnectionFailed
		}

		if !exists {
			return nil, model.ErrNotAvailable
		}
	}

	return data, err
}

func (hm *GoCloudUrlStorage) ReserveAndSet(hash []byte, data []byte) error {
	if hm.IsReadOnly() {
		logger.DefaultLogger.Warnf("ReserveAndSet: read only")
		return model.ErrReadOnly
	}

	grp := fmt.Sprintf("ReserveAndSet(*,size:%d)", len(data))
	sw := utils.PerformanceStopWatchStart()
	defer sw.LastStep(grp, "FINAL")

	// force existence of use-tag with our ID
	err := hm.putATag(hash, BLOCK_USE_TAG, false)
	if err != nil {
		logger.DefaultLogger.Warnf("writing to block storage failed! Put reservation. %+v", err)
		return err
	}

	sw.Step("ptTg")

	//existsAlready, err := hm.bucket.Exists(hm.ctx, stringKey)
	//if err != nil {
	//	log.Fatal(err)
	//	return err
	//}
	//if existsAlready {
	//	return // skip upload
	//}

	hashKey := hm.getBlockStringKey(hash)
	err = hm.bucket.WriteAll(hm.ctx, hashKey, data, nil)
	if err != nil {
		logger.DefaultLogger.Warnf("writing to block storage failed! Write. %+v", err)
		return err
	}

	sw.Step("wrtAll")
	return nil
}

func (hm *GoCloudUrlStorage) DeleteReservation(hash []byte) error {
	// delete reference to block data for this node
	// TODO: trigger async immediate checked delete?
	err := hm.removeATag(hash, BLOCK_USE_TAG)
	if err != nil {
		log.Fatal(err)
		return err
	}

	return nil
}

func (hm *GoCloudUrlStorage) GetMeta(name string) (data []byte, err error) {
	return hm.bucket.ReadAll(hm.ctx, getMetadataStringKey(name))
}

func (hm *GoCloudUrlStorage) SetMeta(name string, data []byte) error {
	if hm.IsReadOnly() {
		logger.DefaultLogger.Warnf("SetMeta: read only")
		return model.ErrReadOnly
	}

	return hm.bucket.WriteAll(hm.ctx, getMetadataStringKey(name), data, nil)
}
func (hm *GoCloudUrlStorage) DeleteMeta(name string) error {
	if hm.IsReadOnly() {
		logger.DefaultLogger.Warnf("SetMeta: read only")
		return model.ErrReadOnly
	}

	return hm.bucket.Delete(hm.ctx, getMetadataStringKey(name))
}

func (hm *GoCloudUrlStorage) Close() error {
	return hm.bucket.Close()
}

type HashStateAndError struct {
	d   model.HashAndState
	err error
}

func (hm *GoCloudUrlStorage) IterateBlocks(ctx context.Context, fn func(d model.HashAndState)) error {

	numberOfParallelRequests := 2
	numberOfParallelConnections := 1
	chanOfChannels := make(chan chan HashStateAndError, numberOfParallelRequests-1)

	connections := make([]*GoCloudUrlStorage, 0, numberOfParallelConnections)
	connections = append(connections, hm)
	for i := 0; i < numberOfParallelConnections-1; i++ {
		hmParallel := NewGoCloudUrlStorage(ctx, hm.url, hm.myDeviceId, hm.hashStringStrategy)
		defer hmParallel.Close()
		connections = append(connections, hmParallel)
	}

	go func() {
		defer close(chanOfChannels)
		// do iterations in chunks for better scalability.
		for i := 0; i < 256; i++ {

			if utils.IsDone(ctx) {
				return
			}

			b := byte(i)
			b_str := hashutil.HashToStringMapKey([]byte{b})
			partChannel := make(chan HashStateAndError)
			chanOfChannels <- partChannel
			go func() {
				defer close(partChannel)
				hmIdx := i % numberOfParallelConnections
				err := connections[hmIdx].IterateBlocksInternal(ctx, b_str, func(d model.HashAndState) {
					partChannel <- HashStateAndError{d, nil}
				})

				if err != nil {
					partChannel <- HashStateAndError{model.HashAndState{}, err}
					return
				}
			}()
		}
	}()

	for channel := range chanOfChannels {

		if utils.IsDone(ctx) {
			return nil
		}

		// logger.DefaultLogger.Infof("processing channel: %+v", channel)
		for d := range channel {

			if utils.IsDone(ctx) {
				return nil
			}

			// logger.DefaultLogger.Infof("processing channel entry: %+v", d)
			if d.err != nil {
				return d.err
			}

			fn(d.d)
		}
	}

	return nil
}

func (hm *GoCloudUrlStorage) IterateBlocksInternal(
	ctx context.Context, prefix string, fn func(d model.HashAndState)) error {

	iterator := NewHashBlockStorageMapBuilder(hm.myDeviceId,
		func(str string) []byte {
			return hm.hashStringStrategy.SlashedStringMapKeyToHashNoError(str)
		},
		func(d model.HashAndState) {
			fn(d)
		})

	folderPrefix := BlockDataSubFolder + "/"
	perPageCount := 1024 * 4
	opts := &blob.ListOptions{}
	opts.Prefix = folderPrefix + prefix
	pageToken := blob.FirstPageToken
	i := 0
	for {
		if utils.IsDone(ctx) {
			return context.Canceled
		}

		// logger.DefaultLogger.Infof("prefix %v - loading page #%v ... ", prefix, i)
		i += 1
		page, nextPageToken, err := hm.bucket.ListPage(hm.ctx, pageToken, perPageCount, opts)
		if err != nil {
			return err
		}

		for _, obj := range page {
			hashString, _ := strings.CutPrefix(obj.Key, folderPrefix)
			elements := strings.Split(hashString, ".")
			if len(elements) <= 1 {
				iterator.addData(hashString)
			} else {
				hashString = elements[0]
				tp := elements[1]
				if tp == BLOCK_USE_TAG && len(elements) >= 3 {
					deviceId := elements[2]
					iterator.addUse(hashString, deviceId)
				} else if tp == BLOCK_DELETE_TAG {
					// ignore deletes that are older than a minute as they are outdated/left overs, TODO: use constant
					if time.Since(obj.ModTime) < TIME_CONSTANT_BASE {
						iterator.addDelete(hashString)
					}
				}
			}

			if utils.IsDone(ctx) {
				return context.Canceled
			}
		}

		if len(nextPageToken) == 0 {
			// DONE
			iterator.close()
			return nil
		}

		pageToken = nextPageToken
	}
}

func (hm *GoCloudUrlStorage) IterateSubdirs(prefix string, delimiter string, fn func(e *model.ListObject)) error {
	listCtx, listCtxCancel := context.WithCancel(context.Background())
	defer listCtxCancel()
	opts := &blob.ListOptions{Prefix: prefix, Delimiter: delimiter}
	token := blob.FirstPageToken
	for {
		var page []*blob.ListObject
		var err error
		page, token, err = hm.bucket.ListPage(listCtx, token, 100, opts)
		if err != nil {
			return err
		}

		if len(page) == 0 {
			return nil
		}

		for _, e := range page {
			fn(&model.ListObject{
				Key:  []byte(e.Key),
				Size: e.Size,
			})
		}
	}
}
