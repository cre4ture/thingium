// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package blockstorage

import (
	"context"
	"fmt"

	"github.com/syncthing/syncthing/lib/model"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/iterator"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

type BsLevelDB struct {
	ldb *leveldb.DB
}

func NewBsLevelDB(location string) *BsLevelDB {
	ldb, err := leveldb.OpenFile(location, nil)
	if err != nil {
		panic(fmt.Sprintf("Failed to open leveldb at %s: %v", location, err))
	}
	return &BsLevelDB{ldb: ldb}
}
func (b *BsLevelDB) Close() error {
	return b.ldb.Close()
}
func (b *BsLevelDB) Get(key []byte) ([]byte, error) {
	val, err := b.ldb.Get(key, nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	return val, nil
}
func (b *BsLevelDB) Put(key, val []byte) error {
	err := b.ldb.Put(key, val, nil)
	if err != nil {
		return err
	}
	return nil
}
func (b *BsLevelDB) Delete(key []byte) error {
	err := b.ldb.Delete(key, nil)
	if err != nil {
		return err
	}
	return nil
}
func (b *BsLevelDB) Has(key []byte) (bool, error) {
	val, err := b.ldb.Has(key, nil)
	if err != nil {
		return false, err
	}
	return val, nil
}

func getDataKey(hash []byte) []byte {
	return append([]byte("d/"), hash...)
}
func getMetaKey(key []byte) []byte {
	return append([]byte("m/"), key...)
}

// implementation of model.HashBlockStorageI for BsLevelDB

func (b *BsLevelDB) CalculateInternalPathRepresentationFromHash(hash []byte) string {
	// Example implementation: convert hash to a hex string
	return fmt.Sprintf("%x", hash)
}

func (b *BsLevelDB) ReserveAndGet(hash []byte, downloadData bool) ([]byte, error) {
	// Example implementation: fetch data and simulate reservation
	var data []byte
	var err error
	if downloadData {
		data, err = b.Get(getDataKey(hash))
	} else {
		_, err = b.Has(getDataKey(hash))
	}
	if err != nil {
		return nil, err
	}
	if downloadData && (data == nil) {
		return nil, model.ErrNotAvailable
	}
	return data, nil
}

func (b *BsLevelDB) UncheckedGet(hash []byte, downloadData bool) ([]byte, error) {
	// Example implementation: fetch data without reservation logic
	return b.Get(getDataKey(hash))
}

func (b *BsLevelDB) ReserveAndSet(hash []byte, data []byte) error {
	// Example implementation: store data and simulate reservation
	return b.Put(getDataKey(hash), data)
}

func (b *BsLevelDB) DeleteReservation(hash []byte) error {
	// Example implementation: simulate reservation deletion
	return nil
}

func (b *BsLevelDB) AnnounceDelete(hash []byte) error {
	// Example implementation: mark data for deletion
	return nil
}

func (b *BsLevelDB) DeAnnounceDelete(hash []byte) error {
	// Example implementation: undo deletion announcement
	return nil
}

func (b *BsLevelDB) UncheckedDelete(hash []byte) error {
	// Example implementation: delete data without additional checks
	return b.Delete(getDataKey(hash))
}

func (b *BsLevelDB) GetMeta(name string) ([]byte, error) {
	// Example implementation: fetch metadata
	return b.Get(getMetaKey([]byte(name)))
}

func (b *BsLevelDB) SetMeta(name string, data []byte) error {
	// Example implementation: store metadata
	return b.Put(getMetaKey([]byte(name)), data)
}

func (b *BsLevelDB) DeleteMeta(name string) error {
	// Example implementation: delete metadata
	return b.Delete(getMetaKey([]byte(name)))
}

func (b *BsLevelDB) getDataIterator() iterator.Iterator {

	opts := &opt.ReadOptions{
		DontFillCache: true,
		Strict:        0,
	}
	slice := &util.Range{
		Start: []byte("d/"),
		Limit: []byte("e/"),
	}
	return b.ldb.NewIterator(slice, opts)
}

func (b *BsLevelDB) GetBlockHashesCountHint() (int, error) {

	// Use a snapshot to avoid counting errors
	iter := b.getDataIterator()
	defer iter.Release()

	count := 0
	for iter.Next() {
		count++
	}
	return count, iter.Error()
}

func (b *BsLevelDB) GetBlockHashesCache(ctx context.Context, progressNotifier func(count int, currentHash []byte)) (model.HashBlockStateMap, error) {
	// Example implementation: iterate over all hashes and return their states
	stateMap := make(model.HashBlockStateMap)
	iter := b.getDataIterator()
	defer iter.Release()

	count := 0
	for iter.Next() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		key := iter.Key()
		hash := key[2:] // Skip the prefix "d/"
		strHash := fmt.Sprintf("%x", hash)

		state := model.HashBlockState{
			DataExists:       true,
			ReservedByMe:     true,
			ReservedByOthers: false,
			DeletionPending:  false,
		}

		stateMap[strHash] = state
		count++
		if progressNotifier != nil {
			progressNotifier(count, hash)
		}
	}
	return stateMap, iter.Error()
}

func (b *BsLevelDB) GetBlockHashState(hash []byte) (model.HashBlockState, error) {
	val, err := b.Get(hash)
	if err != nil {
		return model.HashBlockState{}, err
	}
	if val == nil {
		return model.HashBlockState{}, nil
	}
	state := model.HashBlockState{
		DataExists:       true,
		ReservedByMe:     true,
		ReservedByOthers: false,
		DeletionPending:  false,
	}
	return state, nil
}
