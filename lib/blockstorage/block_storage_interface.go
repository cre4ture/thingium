// Copyright (C) 2024 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package blockstorage

import "io"

type HashBlockState int

const (
	HBS_NOT_AVAILABLE HashBlockState = iota // internal use only, will not be propagated to outside
	HBS_AVAILABLE
	HBS_AVAILABLE_HOLD
)

type HashBlockStateMap map[string]HashBlockState

type HashBlockStorageI interface {
	io.Closer

	// just Has() alone is not allowed as this doesn't allow proper reference counting
	// Has(hash []byte) (ok bool)

	ReserveAndGet(hash []byte, downloadData bool) (data []byte, ok bool)
	ReserveAndSet(hash []byte, data []byte)
	DeleteReservation(hash []byte)
	GetMeta(name string) (data []byte, ok bool)
	SetMeta(name string, data []byte)
	DeleteMeta(name string)

	// internal use only - so far
	//IterateBlocks(fn func(hash []byte, state HashBlockState) bool) error
	GetBlockHashesCountHint() int
	GetBlockHashesCache(progressNotifier func(count int, currentHash []byte)) HashBlockStateMap
}

func (s HashBlockState) String() string {
	switch s {
	case HBS_NOT_AVAILABLE:
		return "not-available"
	case HBS_AVAILABLE:
		return "available"
	case HBS_AVAILABLE_HOLD:
		return "available-hold"
	default:
		return "unknown"
	}
}
