// Copyright (C) 2024 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package blockstorage

import (
	"context"
	"io"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/syncthing/syncthing/lib/hashutil"
	"github.com/syncthing/syncthing/lib/logger"
	"gocloud.dev/blob"

	_ "gocloud.dev/blob/azureblob"
	_ "gocloud.dev/blob/fileblob"
	_ "gocloud.dev/blob/gcsblob"
	_ "gocloud.dev/blob/memblob"
	_ "gocloud.dev/blob/s3blob"
)

const BlockDataSubFolder = "blocks"
const MetaDataSubFolder = "meta"
const BLOCK_DELETE_TAG = "delete" // delete tags need to be alphabetically before use tags
const BLOCK_USE_TAG = "uses"

type HashBlockStorageMapBuilder struct {
	cb           func(hash string, state HashBlockState)
	currentHash  string
	currentState HashBlockState
}

func NewHashBlockStorageMapBuilder(cb func(hash string, state HashBlockState)) *HashBlockStorageMapBuilder {
	return &HashBlockStorageMapBuilder{
		cb:           cb,
		currentHash:  "",
		currentState: HBS_NOT_AVAILABLE,
	}
}

func (b *HashBlockStorageMapBuilder) completeIfNextHash(hash string) bool {
	complete := hash != b.currentHash
	if complete {
		if b.currentState != HBS_NOT_AVAILABLE {
			b.cb(b.currentHash, b.currentState)
		}
		b.currentHash = hash
	}
	return false
}

func (b *HashBlockStorageMapBuilder) addData(hash string) {
	if b.completeIfNextHash(hash) {
		return
	}

	b.currentState = HBS_AVAILABLE
}

func (b *HashBlockStorageMapBuilder) addUse(hash string) {
	if b.completeIfNextHash(hash) {
		return
	}

	if b.currentState == HBS_AVAILABLE {
		b.currentState = HBS_AVAILABLE_HOLD
	}
}

func (b *HashBlockStorageMapBuilder) addDelete(hash string) {
	if b.completeIfNextHash(hash) {
		return
	}

	// it will be deleted soon
	b.currentState = HBS_NOT_AVAILABLE
}

type GoCloudUrlStorage struct {
	io.Closer

	ctx        context.Context
	bucket     *blob.Bucket
	myDeviceId string
}

// GetBlockHashesCountHint implements HashBlockStorageI.
func (hm *GoCloudUrlStorage) GetBlockHashesCountHint() int {

	minimum := 100

	logger.DefaultLogger.Debugf("GetBlockHashesCountHint() - enter")
	data, ok := hm.GetMeta("BlockCountHint")
	if !ok {
		logger.DefaultLogger.Infof("GetBlockHashesCountHint() - read hint failed - use %v", minimum)
		return minimum
	}

	hint, err := strconv.Atoi(string(data[:]))
	if err != nil {
		logger.DefaultLogger.Infof("GetBlockHashesCountHint() - parsing of hint failed (%v) - use %v", err, minimum)
		return minimum
	}

	if hint < minimum {
		hint = minimum
	}

	logger.DefaultLogger.Infof("GetBlockHashesCountHint() - read hint OK: %v", hint)
	return hint
}

func NewGoCloudUrlStorage(ctx context.Context, url string) *GoCloudUrlStorage {
	bucket, err := blob.OpenBucket(context.Background(), url)
	if err != nil {
		log.Fatal(err)
	}

	instance := &GoCloudUrlStorage{
		ctx:    ctx,
		bucket: bucket,
	}

	return instance
}

func getBlockStringKey(hash []byte) string {
	return BlockDataSubFolder + "/" + hashutil.HashToStringMapKey(hash)
}

func getMetadataStringKey(name string) string {
	return MetaDataSubFolder + "/" + name
}

func (hm *GoCloudUrlStorage) GetBlockHashesCache(progressNotifier func(int)) HashBlockStateMap {

	hashSet := make(map[string]HashBlockState)
	err := hm.IterateBlocks(func(hash []byte, state HashBlockState) bool {

		hashString := hashutil.HashToStringMapKey(hash)
		hashSet[hashString] = state
		logger.DefaultLogger.Infof("IterateBlocks hash(hash, state): %v, %v", hashString, state)
		progressNotifier(len(hashSet))

		select {
		case <-hm.ctx.Done():
			return false
		default:
			return true
		}
	})

	if err != nil {
		logger.DefaultLogger.Warnf("IterateBlocks returned error: %v", err)
		return nil
	}

	blockCountHint := strconv.Itoa(len(hashSet))
	hm.SetMeta("BlockCountHint", []byte(blockCountHint))
	logger.DefaultLogger.Debugf("SetMeta(BlockCountHint): %v", blockCountHint)
	return hashSet
}

//func (hm *GoCloudUrlStorage) Has(hash []byte) (ok bool) {
//	if len(hash) == 0 {
//		return false
//	}
//
//	stringKey := getBlockStringKey(hash)
//
//	hm.bucket.ListPage(hm.ctx, nil, 3)
//	exists, err := hm.bucket.Exists(hm.ctx, stringKey)
//	if gcerrors.Code(err) == gcerrors.NotFound || !exists {
//		return false
//	}
//
//	if err != nil {
//		log.Fatal(err)
//		panic("failed to get block from block storage")
//	}
//
//	return true
//}

func (hm *GoCloudUrlStorage) reserveAndCheckExistence(hash []byte) (ok bool, retry bool) {
	DELETE_TAG := "." + BLOCK_DELETE_TAG + "." // delete tags need to be alphabetically before use tags
	USE_TAG := "." + BLOCK_USE_TAG + "."

	hashKey := getBlockStringKey(hash)
	hashDeviceUseKey := hashKey + USE_TAG + hm.myDeviceId
	// force existence of use-tag with our ID
	err := hm.bucket.WriteAll(hm.ctx, hashDeviceUseKey, []byte{}, nil)
	if err != nil {
		return false, false
	}

	perPageCount := 10 // want to see any "delete" token as well.
	opts := &blob.ListOptions{}
	opts.Prefix = BlockDataSubFolder + "/" + hashKey
	page, _, err := hm.bucket.ListPage(hm.ctx, blob.FirstPageToken, perPageCount, opts)
	if err != nil {
		return false, false
	}

	usesMap := map[string]*blob.ListObject{}
	deletesMap := map[string]*blob.ListObject{}
	var dataEntry *blob.ListObject = nil
	for _, entry := range page {
		suffix, _ := strings.CutPrefix(entry.Key, opts.Prefix)
		if len(suffix) == 0 {
			dataEntry = entry
		} else if deviceId, ok := strings.CutPrefix(suffix, USE_TAG); ok {
			usesMap[deviceId] = entry
		} else if deviceId, ok := strings.CutPrefix(suffix, DELETE_TAG); ok {
			deletesMap[deviceId] = entry
		} else {
			logger.DefaultLogger.Debugf("Object with unknown suffix(key, tag): %v, %v", entry.Key, suffix)
		}
	}

	if len(deletesMap) > 0 {
		// wait until all deletes are processed completely.
		// This should be very rarely happing, thus a simple retry later should not
		// influence overall performance
		return false, true
	}

	if dataEntry == nil {
		return false, false
	}

	return true, false
}

func (hm *GoCloudUrlStorage) Get(hash []byte) (data []byte, ok bool) {
	if len(hash) == 0 {
		return nil, false
	}

	for {
		retry := false
		ok, retry = hm.reserveAndCheckExistence(hash)
		if !retry {
			break
		}
		time.Sleep(time.Minute * 1)
	}

	if ok {
		var err error = nil
		data, err = hm.bucket.ReadAll(hm.ctx,
			BlockDataSubFolder+"/"+hashutil.HashToStringMapKey(hash))
		if err != nil {
			panic("failed to read existing block data!")
		}
	}

	return data, ok
}

func (hm *GoCloudUrlStorage) Set(hash []byte, data []byte) {
	stringKey := getBlockStringKey(hash)
	//existsAlready, err := hm.bucket.Exists(hm.ctx, stringKey)
	//if err != nil {
	//	log.Fatal(err)
	//	panic("writing to block storage failed! Pre-Check.")
	//}
	//if existsAlready {
	//	return // skip upload
	//}
	err := hm.bucket.WriteAll(hm.ctx, stringKey, data, nil)
	if err != nil {
		log.Fatal(err)
		panic("writing to block storage failed! Write.")
	}
}

func (hm *GoCloudUrlStorage) Delete(hash []byte) {
	err := hm.bucket.Delete(hm.ctx, getBlockStringKey(hash))
	if err != nil {
		log.Fatal(err)
		panic("writing to block storage failed!")
	}
}

func (hm *GoCloudUrlStorage) GetMeta(name string) (data []byte, ok bool) {
	data, err := hm.bucket.ReadAll(hm.ctx, getMetadataStringKey(name))
	if err != nil {
		return nil, false
	}
	return data, true
}
func (hm *GoCloudUrlStorage) SetMeta(name string, data []byte) {
	hm.bucket.WriteAll(hm.ctx, getMetadataStringKey(name), data, nil)
}
func (hm *GoCloudUrlStorage) DeleteMeta(name string) {
	hm.bucket.Delete(hm.ctx, getMetadataStringKey(name))
}

func (hm *GoCloudUrlStorage) Close() error {
	return hm.bucket.Close()
}

func (hm *GoCloudUrlStorage) IterateBlocks(fn func(hash []byte, state HashBlockState) bool) error {

	stopRequested := false
	iterator := NewHashBlockStorageMapBuilder(func(hashStr string, state HashBlockState) {
		stopRequested = fn(hashutil.StringMapKeyToHashNoError(hashStr), state)
	})

	perPageCount := 1024 * 4
	opts := &blob.ListOptions{}
	opts.Prefix = BlockDataSubFolder + "/"
	pageToken := blob.FirstPageToken
	i := 0
	for {
		logger.DefaultLogger.Infof("loading page #%v ... ", i)
		i += 1
		page, nextPageToken, err := hm.bucket.ListPage(hm.ctx, pageToken, perPageCount, opts)
		if err != nil {
			return err
		}
		if len(nextPageToken) == 0 {
			// DONE
			return nil
		}

		for _, obj := range page {
			hashString, _ := strings.CutPrefix(obj.Key, opts.Prefix)
			elements := strings.Split(hashString, ".")
			if len(elements) <= 1 {
				iterator.addData(hashString)
			} else {
				tp := elements[1]
				if tp == BLOCK_USE_TAG && len(elements) >= 3 {
					deviceId := elements[2]
					if deviceId == hm.myDeviceId {
						iterator.addUse(hashString)
					}
				} else if tp == BLOCK_DELETE_TAG {
					iterator.addDelete(hashString)
				}
			}
			if stopRequested {
				return nil
			}
		}

		pageToken = nextPageToken
	}
}
