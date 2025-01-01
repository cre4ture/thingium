// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package utils

import (
	"context"
)

type safeWorkGroupInternals struct {
	maxPending      int
	nextUnusedToken uint64
	pendingTokens   map[uint64]struct{}
	cancel          context.CancelFunc
}

type SafeWorkGroup struct {
	internals   *ProtectedCond[safeWorkGroupInternals]
	workOngoing context.Context
}

func NewSafeWorkGroup(ctx context.Context, maxPending int) *SafeWorkGroup {
	ctx, cancel := context.WithCancel(ctx)
	return &SafeWorkGroup{
		internals: NewProtectedCond[safeWorkGroupInternals](safeWorkGroupInternals{
			maxPending:      maxPending,
			nextUnusedToken: 0,
			pendingTokens:   make(map[uint64]struct{}),
			cancel:          cancel,
		}),
		workOngoing: ctx,
	}
}

// Run will call the callback fn synchronously and pass a done function that must be called when the work is done.
// The done function can be called from an asynchronous goroutine.
func (swg *SafeWorkGroup) Run(fn func(done func(), ctx context.Context)) {
	token := swg.GetToken()
	fn(func() {
		swg.ReleaseToken(token)
	}, swg.workOngoing)
}

// Go will call the callback fn asynchronously and return the token automatically after the callback is done.
func (swg *SafeWorkGroup) Go(fn func(ctx context.Context)) {
	token := swg.GetToken()
	go func() {
		defer swg.ReleaseToken(token)
		fn(swg.workOngoing)
	}()
}

func (swg *SafeWorkGroup) GetToken() uint64 {
	intern := swg.internals.Lock()
	defer swg.internals.UnlockNoSignal()

	for {
		if (intern.maxPending > 0) && (len(intern.pendingTokens) >= intern.maxPending) {
			swg.internals.cond.Wait()
		} else {
			break
		}
	}

	// use a fixed xor token to avoid accidental token guessing
	xorToken := uint64(0x2ab592cf9922445d)
	newToken := intern.nextUnusedToken ^ xorToken
	intern.nextUnusedToken += 1
	intern.pendingTokens[newToken] = struct{}{}

	return newToken
}

func (intern *safeWorkGroupInternals) IsDone(ctx context.Context) bool {
	return len(intern.pendingTokens) == 0 && IsDone(ctx)
}

func (swg *SafeWorkGroup) ReleaseToken(token uint64) {
	intern := swg.internals.Lock()
	delete(intern.pendingTokens, token)
	swg.internals.UnlockWithBroadcast()
}

func (swg *SafeWorkGroup) WaitNoClose() {
	intern := swg.internals.Lock()
	defer swg.internals.UnlockNoSignal()
	for {
		if intern.IsDone(swg.workOngoing) {
			break
		}
		swg.internals.cond.Wait()
	}
}

func (swg *SafeWorkGroup) CloseAndWait() {
	intern := swg.internals.Lock()
	defer swg.internals.UnlockNoSignal()
	intern.cancel()
	for {
		if intern.IsDone(swg.workOngoing) {
			break
		}
		swg.internals.cond.Wait()
	}
}
