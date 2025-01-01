// Copyright (C) 2024 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/utils"
)

var ErrConnectionFailed = errors.New("connection to bucket failed. Retry later again")
var ErrNotAvailable = errors.New("bucket entry doesn't exist")
var ErrRetryLater = errors.New("bucket entry is blocked by pending delete. Retry later")
var ErrReadOnly = errors.New("bucket access is limited to read only")

type HashBlockState struct {
	DataExists       bool
	ReservedByMe     bool
	ReservedByOthers bool
	DeletionPending  bool
}

type HashAndState struct {
	Hash  []byte
	State HashBlockState
}

func (s HashBlockState) IsAvailable() bool {
	return s.DataExists && (!s.DeletionPending)
}

func (s HashBlockState) IsAvailableAndReservedByMe() bool {
	return s.DataExists && s.ReservedByMe
}

func (s HashBlockState) IsAvailableAndFree() bool {
	return s.DataExists && (!s.ReservedByMe) && (!s.ReservedByOthers)
}

func (s HashBlockState) IsReservedBySomeone() bool {
	return s.DataExists && (s.ReservedByMe || s.ReservedByOthers)
}

type Hash [32]byte

type HashSequence struct {
	sequence     []Hash
	sequenceHash Hash
}

type HashBlockStateMap map[string]HashBlockState

type HashBlockStorageI interface {
	io.Closer

	// just Has() alone is not allowed as this doesn't allow proper reference counting
	// Has(hash []byte) (ok bool)

	CalculateInternalPathRepresentationFromHash(hash []byte) string

	ReserveAndGet(hash []byte, downloadData bool) (data []byte, err error)
	UncheckedGet(hash []byte, downloadData bool) (data []byte, err error)

	ReserveAndSet(hash []byte, data []byte) error
	DeleteReservation(hash []byte) error
	AnnounceDelete(hash []byte) error
	DeAnnounceDelete(hash []byte) error
	UncheckedDelete(hash []byte) error
	GetMeta(name string) (data []byte, err error)
	SetMeta(name string, data []byte) error
	DeleteMeta(name string) error

	// internal use only - so far
	//IterateBlocks(fn func(hash []byte, state HashBlockState) bool) error
	GetBlockHashesCountHint() (int, error)
	GetBlockHashesCache(ctx context.Context, progressNotifier func(count int, currentHash []byte)) (HashBlockStateMap, error)
	GetBlockHashState(hash []byte) (HashBlockState, error)
}

func GetBlockDataFromCacheOrDownload(
	connection HashBlockStorageI,
	file protocol.FileInfo,
	block protocol.BlockInfo,
	downloadBlockDataCb func(block protocol.BlockInfo) ([]byte, error),
	checkOnly bool,
) ([]byte, error, GetBlockDataResult) {
	grp := fmt.Sprintf("ff-GetBlockDataFromCacheOrDownload(%v:%v): START", file.Name, block.Offset/int64(file.BlockSize()))
	watch := utils.PerformanceStopWatchStart()
	defer watch.LastStep(grp, "FINAL")

	data, err := connection.ReserveAndGet(block.Hash, checkOnly)
	if err == nil {
		return data, nil, GET_BLOCK_CACHED
	} else {
		if !errors.Is(err, ErrNotAvailable) {
			// connection error, or other unknown issue
			return nil, err, GET_BLOCK_FAILED
		}
	}

	watch.Step("rsvAndGt")

	downloadBlockDataCb(block)

	watch.Step("pull")

	connection.ReserveAndSet(block.Hash, data)

	watch.Step("rsvAndSt")

	return data, nil, GET_BLOCK_DOWNLOAD
}
