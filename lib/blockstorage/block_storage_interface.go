// Copyright (C) 2024 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package blockstorage

import (
	"context"
	"io"
)

type HashBlockState struct {
	dataExists       bool
	reservedByMe     bool
	reservedByOthers bool
	deletionPending  bool
}

func (s HashBlockState) IsAvailable() bool {
	return s.dataExists && (!s.deletionPending)
}

func (s HashBlockState) IsAvailableAndReservedByMe() bool {
	return s.dataExists && s.reservedByMe
}

func (s HashBlockState) IsAvailableAndFree() bool {
	return s.dataExists && (!s.reservedByMe) && (!s.reservedByOthers)
}

func (s HashBlockState) IsReservedBySomeone() bool {
	return s.dataExists && (s.reservedByMe || s.reservedByOthers)
}

type HashBlockStateMap map[string]HashBlockState

type HashBlockStorageI interface {
	io.Closer

	// just Has() alone is not allowed as this doesn't allow proper reference counting
	// Has(hash []byte) (ok bool)

	ReserveAndGet(hash []byte, downloadData bool) (data []byte, ok bool)
	ReserveAndSet(hash []byte, data []byte)
	DeleteReservation(hash []byte)
	AnnounceDelete(hash []byte) error
	DeAnnounceDelete(hash []byte) error
	UncheckedDelete(hash []byte) error
	GetMeta(name string) (data []byte, ok bool)
	SetMeta(name string, data []byte)
	DeleteMeta(name string)

	// internal use only - so far
	//IterateBlocks(fn func(hash []byte, state HashBlockState) bool) error
	GetBlockHashesCountHint() int
	GetBlockHashesCache(ctx context.Context, progressNotifier func(count int, currentHash []byte)) HashBlockStateMap
	GetBlockHashState(hash []byte) HashBlockState
}
