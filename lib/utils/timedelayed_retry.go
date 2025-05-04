// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package utils

import (
	"context"
	"time"
)

func AbortableTimeDelayedRetryWithReturnValue[T any](
	ctx context.Context,
	tries uint,
	waitDurationTotal time.Duration,
	fn func(tryNr uint) (T, error),
) (T, error) {
	tryNr := uint(0)
	waitDurationStep := waitDurationTotal / time.Duration(tries)
	for {
		value, err := fn(tryNr)
		if err == nil {
			return value, nil
		}

		tryNr += 1
		err = AbortableTimeSleep(ctx, waitDurationStep)
		if err != nil {
			// it was aborted
			return value, err
		}
	}
}

func AbortableTimeDelayedRetry(
	ctx context.Context,
	tries uint,
	waitDurationTotal time.Duration,
	fn func(tryNr uint) error,
) error {
	tryNr := uint(0)
	waitDurationStep := waitDurationTotal / time.Duration(tries)
	for {
		err := fn(tryNr)
		if err == nil {
			return nil
		}

		tryNr += 1
		err = AbortableTimeSleep(ctx, waitDurationStep)
		if err != nil {
			// it was aborted
			return err
		}
	}
}
