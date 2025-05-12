// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package utils

import (
	"sync"
)

type ParallelLeases struct {
	jobGroupName string
	count        uint
	freeLeases   uint
	aborting     bool
	cond         *sync.Cond
}

func NewParallelLeases(count uint, jobGroupName string) *ParallelLeases {
	return &ParallelLeases{
		jobGroupName: jobGroupName,
		count:        count,
		freeLeases:   count,
		aborting:     false,
		cond:         sync.NewCond(&sync.Mutex{}),
	}
}

func (pl *ParallelLeases) AsyncRunOneWithDoneFn(name string, fn func(doneFn func())) {
	pl.cond.L.Lock()
	defer pl.cond.L.Unlock()

	// get and potentially wait for free lease
	var currentlyFree uint
	for {
		if pl.aborting {
			return
		}

		if pl.freeLeases > 0 {
			pl.freeLeases -= 1
			currentlyFree = pl.freeLeases
			break
		}

		pl.cond.Wait()
	}

	_ = currentlyFree
	//logger.DefaultLogger.Infof("START[%v, %v] leases free: %v/%v", pl.jobGroupName, name, currentlyFree, pl.count)
	go fn(func() {
		// return lease
		pl.cond.L.Lock()
		defer pl.cond.L.Unlock()
		pl.freeLeases += 1
		pl.cond.Signal()
		//logger.DefaultLogger.Infof("DONE[%v, %v] leases free: %v/%v", pl.jobGroupName, name, currentlyFree, pl.count)
	})
}

func (pl *ParallelLeases) AsyncRunOne(name string, fn func()) {
	pl.AsyncRunOneWithDoneFn(name, func(doneFn func()) {
		defer doneFn()
		fn()
	})
}

func (pl *ParallelLeases) AbortAndWait() {
	//defer logger.DefaultLogger.Infof("PULL_X: wait for async operations to complete - DONE")
	func() {
		pl.cond.L.Lock()
		defer pl.cond.L.Unlock()
		pl.aborting = true
		pl.cond.Broadcast()
	}()

	pl.WaitAllFinished()
}

func (pl *ParallelLeases) WaitAllFinished() {
	//defer logger.DefaultLogger.Infof("PULL_X: wait for async operations to complete - DONE")
	pl.cond.L.Lock()
	defer pl.cond.L.Unlock()

	// wait for async operations to complete
	for {
		//logger.DefaultLogger.Infof("PULL_X: wait for async operations to complete ... %v/%v", pl.freeLeases, pl.count)
		if pl.freeLeases == pl.count {
			return
		}

		pl.cond.Wait()
	}
}
