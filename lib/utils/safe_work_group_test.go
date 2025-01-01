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

func TestSafeWorkgroup(t *testing.T) {
	wg := utils.NewSafeWorkGroup(context.Background(), 0)
	counter := atomic.Int64{}

	for i := 0; i < 10; i++ {
		counter.Add(1)
		wg.Run(func(done func(), ctx context.Context) {
			counter.Add(-1)
			done()
		})
	}

	// all goroutine are done
	// still wait will block
	isDone := atomic.Bool{}
	go func() {
		wg.WaitNoClose()
		isDone.Store(true)
	}()

	time.Sleep(10 * time.Millisecond)

	assert.False(t, isDone.Load())

	for i := 0; i < 10; i++ {
		counter.Add(1)
		wg.Go(func(ctx context.Context) {
			time.Sleep(50 * time.Millisecond)
			counter.Add(-1)
		})
	}

	assert.NotEqual(t, 0, counter.Load())

	wg.CloseAndWait()

	assert.Equal(t, int64(0), counter.Load())

	time.Sleep(10 * time.Millisecond)

	assert.True(t, isDone.Load())
}
