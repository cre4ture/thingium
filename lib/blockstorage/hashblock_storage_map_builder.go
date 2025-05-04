// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.
package blockstorage

import "github.com/syncthing/syncthing/lib/model"

type HashBlockStorageMapBuilder struct {
	ownName      string
	cb           func(d model.HashAndState)
	currentHash  string
	currentState model.HashBlockState
	strToHashFn  func(string) []byte
}

func NewHashBlockStorageMapBuilder(ownName string, strToHashFn func(string) []byte, cb func(d model.HashAndState)) *HashBlockStorageMapBuilder {
	return &HashBlockStorageMapBuilder{
		ownName:      ownName,
		cb:           cb,
		currentHash:  "",
		currentState: model.HashBlockState{},
		strToHashFn:  strToHashFn,
	}
}

func (b *HashBlockStorageMapBuilder) completeIfNextHash(hash string) bool {
	complete := hash != b.currentHash
	if complete {
		if b.currentState.DataExists {
			b.cb(model.HashAndState{b.strToHashFn(b.currentHash), b.currentState})
		}
		b.currentHash = hash
		b.currentState = model.HashBlockState{}
	}
	return false
}

func (b *HashBlockStorageMapBuilder) addData(hash string) {
	if b.completeIfNextHash(hash) {
		return
	}

	b.currentState.DataExists = true
}

func (b *HashBlockStorageMapBuilder) addUse(hash string, who string) {
	if b.completeIfNextHash(hash) {
		return
	}

	if b.currentState.IsAvailable() {
		isMe := who == b.ownName
		if isMe {
			b.currentState.ReservedByMe = true
		} else {
			b.currentState.ReservedByOthers = true
		}
	}
}

func (b *HashBlockStorageMapBuilder) addDelete(hash string) {
	if b.completeIfNextHash(hash) {
		return
	}

	// it will be deleted soon
	b.currentState.DeletionPending = true
}

func (b *HashBlockStorageMapBuilder) close() {
	b.completeIfNextHash("") // force flush of last element
}
