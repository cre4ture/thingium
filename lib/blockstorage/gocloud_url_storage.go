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

	"github.com/syncthing/syncthing/lib/hashutil"
	"github.com/syncthing/syncthing/lib/logger"
	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"

	_ "gocloud.dev/blob/azureblob"
	_ "gocloud.dev/blob/fileblob"
	_ "gocloud.dev/blob/gcsblob"
	_ "gocloud.dev/blob/memblob"
	_ "gocloud.dev/blob/s3blob"
)

const BlockDataSubFolder = "blocks"
const MetaDataSubFolder = "meta"

type GoCloudUrlStorage struct {
	io.Closer

	ctx    context.Context
	bucket *blob.Bucket

	existenceCache map[string]struct{}
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

	instance.fillCache()

	return instance
}

func getBlockStringKey(hash []byte) string {
	return BlockDataSubFolder + "/" + hashutil.HashToStringMapKey(hash)
}

func getMetadataStringKey(name string) string {
	return MetaDataSubFolder + "/" + name
}

func (hm *GoCloudUrlStorage) fillCache() {
	dummyValue := struct{}{}
	hashSet := make(map[string]struct{})
	err := hm.IterateBlocks(func(hash []byte) bool {
		hashSet[hashutil.HashToStringMapKey(hash)] = dummyValue
		select {
		case <-hm.ctx.Done():
			return false
		default:
			return true
		}
	})

	if err != nil {
		logger.DefaultLogger.Warnf("IterateBlocks returned error: %v", err)
		return
	}

	hm.existenceCache = hashSet
}

func (hm *GoCloudUrlStorage) Has(hash []byte) (ok bool) {
	if len(hash) == 0 {
		return false
	}

	stringKey := getBlockStringKey(hash)

	if hm.existenceCache != nil {
		_, ok := hm.existenceCache[stringKey]
		return ok
	}

	exists, err := hm.bucket.Exists(hm.ctx, stringKey)
	if gcerrors.Code(err) == gcerrors.NotFound || !exists {
		return false
	}

	if err != nil {
		log.Fatal(err)
		panic("failed to get block from block storage")
	}

	return true
}
func (hm *GoCloudUrlStorage) Get(hash []byte) (data []byte, ok bool) {
	if len(hash) == 0 {
		return nil, false
	}

	stringKey := getBlockStringKey(hash)
	data, err := hm.bucket.ReadAll(hm.ctx, stringKey)
	if gcerrors.Code(err) == gcerrors.NotFound {
		return nil, false
	}

	if err != nil {
		log.Fatal(err)
		panic("failed to get block from block storage")
	}

	return data, true
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

func (hm *GoCloudUrlStorage) IterateBlocks(fn func(hash []byte) bool) error {

	opts := &blob.ListOptions{}
	opts.Prefix = BlockDataSubFolder + "/"
	pageToken := blob.FirstPageToken
	for {
		page, nextPageToken, err := hm.bucket.ListPage(hm.ctx, pageToken, 512, opts)
		if err != nil {
			return err
		}
		if len(nextPageToken) == 0 {
			// DONE
			return nil
		}

		for _, obj := range page {
			hash, err := hashutil.StringMapKeyToHash(obj.Key)
			if err != nil {
				continue
			}
			wantNext := fn(hash)
			if !wantNext {
				return nil
			}
		}

		pageToken = nextPageToken
	}
}
