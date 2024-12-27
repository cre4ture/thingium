// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package utils

import "sync"

type Protected[T any] struct {
	t   *T
	mut sync.Mutex
}

func NewProtected[T any](value T) *Protected[T] {
	return &Protected[T]{
		t:   &value,
		mut: sync.Mutex{},
	}
}

func (p *Protected[T]) Lock() *T {
	p.mut.Lock()
	return p.t
}

func (p *Protected[T]) Unlock() {
	p.mut.Unlock()
}
