// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package utils_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/syncthing/syncthing/lib/utils"
)

type testStruct struct {
	workCounter    atomic.Int64
	serviceCounter atomic.Int64
}

func TestServingObject(t *testing.T) {

	workCtx, workCtxCancel := context.WithCancel(context.Background())
	so := utils.NewServingObject(workCtx, &testStruct{workCounter: atomic.Int64{}, serviceCounter: atomic.Int64{}})

	so.ServiceRoutineGo(func(obj *testStruct, ctx context.Context) {
		obj.serviceCounter.Add(1)
		<-ctx.Done()
		obj.serviceCounter.Add(-1)
	})

	time.Sleep(50 * time.Millisecond)

	so.NormalWorkerRun(func(obj *testStruct, done func(), ctx context.Context) {
		defer done()
		assert.Equal(t, int64(0), obj.workCounter.Load())
		assert.Equal(t, int64(1), obj.serviceCounter.Load())
	})

	so.NormalWorkerRun(func(obj *testStruct, done func(), ctx context.Context) {
		obj.workCounter.Add(1)
		go func() {
			defer done()
			<-ctx.Done()
			obj.workCounter.Add(-1)
		}()
	})

	so.NormalWorkerRun(func(obj *testStruct, done func(), ctx context.Context) {
		defer done()
		assert.Equal(t, int64(1), obj.workCounter.Load())
		assert.Equal(t, int64(1), obj.serviceCounter.Load())
	})

	workCtxCancel()

	time.Sleep(50 * time.Millisecond)

	so.NormalWorkerRun(func(obj *testStruct, done func(), ctx context.Context) {
		defer done()
		assert.Equal(t, int64(0), obj.workCounter.Load())
		assert.Equal(t, int64(1), obj.serviceCounter.Load())
	})

	so.Close()

	so.NormalWorkerRun(func(obj *testStruct, done func(), ctx context.Context) {
		defer done()
		assert.Equal(t, int64(0), obj.workCounter.Load())
		assert.Equal(t, int64(0), obj.serviceCounter.Load())
	})
}
