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
const BLOCK_DELETE_TAG = "deletion-by" // delete tags need to be alphabetically before use tags
const BLOCK_USE_TAG = "used-by"

type HashBlockStorageMapBuilder struct {
	ownName      string
	cb           func(hash string, state HashBlockState)
	currentHash  string
	currentState HashBlockState
}

func NewHashBlockStorageMapBuilder(ownName string, cb func(hash string, state HashBlockState)) *HashBlockStorageMapBuilder {
	return &HashBlockStorageMapBuilder{
		ownName:      ownName,
		cb:           cb,
		currentHash:  "",
		currentState: HashBlockState{},
	}
}

func (b *HashBlockStorageMapBuilder) completeIfNextHash(hash string) bool {
	complete := hash != b.currentHash
	if complete {
		if b.currentState.dataExists {
			b.cb(b.currentHash, b.currentState)
		}
		b.currentHash = hash
		b.currentState = HashBlockState{}
	}
	return false
}

func (b *HashBlockStorageMapBuilder) addData(hash string) {
	if b.completeIfNextHash(hash) {
		return
	}

	b.currentState.dataExists = true
}

func (b *HashBlockStorageMapBuilder) addUse(hash string, who string) {
	if b.completeIfNextHash(hash) {
		return
	}

	if b.currentState.IsAvailable() {
		isMe := who == b.ownName
		if isMe {
			b.currentState.reservedByMe = true
		} else {
			b.currentState.reservedByOthers = true
		}
	}
}

func (b *HashBlockStorageMapBuilder) addDelete(hash string) {
	if b.completeIfNextHash(hash) {
		return
	}

	// it will be deleted soon
	b.currentState.deletionPending = true
}

func (b *HashBlockStorageMapBuilder) close() {
	b.completeIfNextHash("") // force flush of last element
}

type GoCloudUrlStorage struct {
	io.Closer

	ctx        context.Context
	bucket     *blob.Bucket
	myDeviceId string
}

func (hm *GoCloudUrlStorage) getATag(hash []byte, tag string) string {
	hashKey := getBlockStringKey(hash)
	return hashKey + "." + tag + "." + hm.myDeviceId
}

func (hm *GoCloudUrlStorage) putATag(hash []byte, tag string) error {
	hashDeviceTagKey := hm.getATag(hash, tag)
	// force existence of stag with our ID
	existsAlready, err := hm.bucket.Exists(hm.ctx, hashDeviceTagKey)
	if err != nil {
		return err
	}
	if tag != BLOCK_USE_TAG { // skip logging for use tags as this spams
		logger.DefaultLogger.Debugf("Put tag %v (exists: %v): %v", tag, existsAlready, hashDeviceTagKey)
	}
	if existsAlready {
		return nil
	}
	return hm.bucket.WriteAll(hm.ctx, hashDeviceTagKey, []byte{}, nil)
}

func (hm *GoCloudUrlStorage) removeATag(hash []byte, tag string) error {
	reservationKey := getBlockStringKey(hash) + "." + tag + "." + hm.myDeviceId
	logger.DefaultLogger.Debugf("removing tag %v: %v", tag, reservationKey)
	return hm.bucket.Delete(hm.ctx, reservationKey)
}

// AnnounceDelete implements HashBlockStorageI.
func (hm *GoCloudUrlStorage) AnnounceDelete(hash []byte) error {
	return hm.putATag(hash, BLOCK_DELETE_TAG)
}

// DeAnnounceDelete implements HashBlockStorageI.
func (hm *GoCloudUrlStorage) DeAnnounceDelete(hash []byte) error {
	return hm.removeATag(hash, BLOCK_DELETE_TAG)
}

// UncheckedDelete implements HashBlockStorageI.
func (hm *GoCloudUrlStorage) UncheckedDelete(hash []byte) error {
	stringKey := getBlockStringKey(hash)
	return hm.bucket.Delete(hm.ctx, stringKey)
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

func NewGoCloudUrlStorage(ctx context.Context, url string, myDeviceId string) *GoCloudUrlStorage {
	bucket, err := blob.OpenBucket(context.Background(), url)
	if err != nil {
		log.Fatal(err)
	}

	instance := &GoCloudUrlStorage{
		ctx:        ctx,
		bucket:     bucket,
		myDeviceId: myDeviceId,
	}

	return instance
}

func getBlockStringKey(hash []byte) string {
	return BlockDataSubFolder + "/" + hashutil.HashToStringMapKey(hash)
}

func getMetadataStringKey(name string) string {
	return MetaDataSubFolder + "/" + name
}

func (hm *GoCloudUrlStorage) GetBlockHashesCache(
	ctx context.Context,
	progressNotifier func(count int, currentHash []byte),
) HashBlockStateMap {

	hashSet := make(map[string]HashBlockState)
	err := hm.IterateBlocks(func(hash []byte, state HashBlockState) bool {

		hashString := hashutil.HashToStringMapKey(hash)
		hashSet[hashString] = state
		// logger.DefaultLogger.Infof("IterateBlocks hash(hash, state): %v, %v", hashString, state)
		progressNotifier(len(hashSet), hash)

		select {
		case <-ctx.Done():
			return false
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

func (hm *GoCloudUrlStorage) GetBlockHashState(hash []byte) HashBlockState {
	blockState := HashBlockState{}
	hm.IterateBlocksInternal(hashutil.HashToStringMapKey(hash), func(hash []byte, state HashBlockState) bool {
		blockState = state
		return true
	})

	return blockState
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
	// force existence of use-tag with our ID
	err := hm.putATag(hash, BLOCK_USE_TAG)
	if err != nil {
		return false, false
	}

	perPageCount := 10 // want to see any "delete" token as well.
	opts := &blob.ListOptions{}
	opts.Prefix = hashKey
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
			//logger.DefaultLogger.Debugf("Object with unknown suffix(key, tag): %v, %v", entry.Key, suffix)
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

func (hm *GoCloudUrlStorage) ReserveAndGet(hash []byte, downloadData bool) (data []byte, ok bool) {
	if len(hash) == 0 {
		return nil, false
	}

	for {
		retry := false
		ok, retry = hm.reserveAndCheckExistence(hash)
		if !retry {
			break
		}
		// wait for a relatively long period of time to allow deletion to complete / skip
		time.Sleep(time.Minute * 1)
	}

	if ok && downloadData {
		var err error = nil
		data, err = hm.bucket.ReadAll(hm.ctx, getBlockStringKey(hash))
		if err != nil {
			panic("failed to read existing block data!")
		}
	}

	return data, ok
}

func (hm *GoCloudUrlStorage) ReserveAndSet(hash []byte, data []byte) {
	// force existence of use-tag with our ID
	err := hm.putATag(hash, BLOCK_USE_TAG)
	if err != nil {
		panic("writing to block storage failed! Put reservation.")
	}

	//existsAlready, err := hm.bucket.Exists(hm.ctx, stringKey)
	//if err != nil {
	//	log.Fatal(err)
	//	panic("writing to block storage failed! Pre-Check.")
	//}
	//if existsAlready {
	//	return // skip upload
	//}

	hashKey := getBlockStringKey(hash)
	err = hm.bucket.WriteAll(hm.ctx, hashKey, data, nil)
	if err != nil {
		log.Fatal(err)
		panic("writing to block storage failed! Write.")
	}
}

func (hm *GoCloudUrlStorage) DeleteReservation(hash []byte) {
	// delete reference to block data for this node
	// TODO: trigger async immediate checked delete?
	err := hm.removeATag(hash, BLOCK_USE_TAG)
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

	// do iterations in chunks for better scalability.
	for i := 0; i < 256; i++ {
		b := byte(i)
		b_str := hashutil.HashToStringMapKey([]byte{b})
		stopRequested, err := hm.IterateBlocksInternal(b_str, fn)
		if stopRequested || (err != nil) {
			return err
		}
	}

	return nil
}

func (hm *GoCloudUrlStorage) IterateBlocksInternal(prefix string, fn func(hash []byte, state HashBlockState) bool) (bool, error) {

	stopRequested := false
	iterator := NewHashBlockStorageMapBuilder(hm.myDeviceId, func(hashStr string, state HashBlockState) {
		stopRequested = !fn(hashutil.StringMapKeyToHashNoError(hashStr), state)
	})

	folderPrefix := BlockDataSubFolder + "/"
	perPageCount := 1024 * 4
	opts := &blob.ListOptions{}
	opts.Prefix = folderPrefix + prefix
	pageToken := blob.FirstPageToken
	i := 0
	for {
		logger.DefaultLogger.Infof("prefix %v - loading page #%v ... ", prefix, i)
		i += 1
		page, nextPageToken, err := hm.bucket.ListPage(hm.ctx, pageToken, perPageCount, opts)
		if err != nil {
			return false, err
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
					iterator.addDelete(hashString)
				}
			}
			if stopRequested {
				return stopRequested, nil
			}
		}

		if len(nextPageToken) == 0 {
			// DONE
			iterator.close()
			return false, nil
		}

		pageToken = nextPageToken
	}
}
