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

// ENUM for download data or check only
type AccessType int

const (
	CHECK_ONLY AccessType = iota
	DOWNLOAD_DATA
)

type HashBlockStorageI interface {
	io.Closer

	// just Has() alone is not allowed as this doesn't allow proper reference counting
	// Has(hash []byte) (ok bool)

	CalculateInternalPathRepresentationFromHash(hash []byte) string

	ReserveAndGet(hash []byte, access AccessType) (data []byte, err error)
	UncheckedGet(hash []byte, access AccessType) (data []byte, err error)

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
	access AccessType,
) ([]byte, error, GetBlockDataResult) {
	grp := fmt.Sprintf("ff-GetBlockDataFromCacheOrDownload(%v:%v): START", file.Name, block.Offset/int64(file.BlockSize()))
	watch := utils.PerformanceStopWatchStartDisabled()
	defer watch.LastStep(grp, "FINAL")

	data, err := connection.ReserveAndGet(block.Hash, access)
	if err == nil {
		watch.Step(fmt.Sprintln("rsvAndGet-return-1-", len(data)))
		return data, nil, GET_BLOCK_CACHED
	}

	if !errors.Is(err, ErrNotAvailable) {
		// connection error, or other unknown issue
		watch.Step("rsvAndGet-return-2")
		return nil, err, GET_BLOCK_FAILED
	}

	// not available

	if access == CHECK_ONLY {
		watch.Step("rsvAndGet-return-3")
		return nil, ErrNotAvailable, GET_BLOCK_FAILED
	}

	watch.Step("rsvAndGt")

	data, err = downloadBlockDataCb(block)
	if err != nil {
		watch.Step("downloadBlockDataCb-return-1")
		return nil, err, GET_BLOCK_FAILED
	}

	watch.Step("pull")

	connection.ReserveAndSet(block.Hash, data)

	watch.Step("rsvAndSt")

	return data, nil, GET_BLOCK_DOWNLOAD
}
