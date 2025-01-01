// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package utils

import "sync"

type ProtectedCond[T any] struct {
	t    *T
	cond *sync.Cond
}

func NewProtectedCond[T any](value T) *ProtectedCond[T] {
	return &ProtectedCond[T]{
		t:    &value,
		cond: sync.NewCond(&sync.Mutex{}),
	}
}

func (p *ProtectedCond[T]) Lock() *T {
	p.cond.L.Lock()
	return p.t
}

func (p *ProtectedCond[T]) UnlockNoSignal() {
	p.cond.Signal()
	p.cond.L.Unlock()
}

func (p *ProtectedCond[T]) UnlockWithSignal() {
	p.cond.Signal()
	p.cond.L.Unlock()
}

func (p *ProtectedCond[T]) UnlockWithBroadcast() {
	p.cond.Broadcast()
	p.cond.L.Unlock()
}
