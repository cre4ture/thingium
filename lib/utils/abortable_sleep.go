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

func AbortableTimeSleep(ctx context.Context, duration time.Duration) error {
	select {
	case <-ctx.Done():
		return context.Canceled
	case <-time.After(duration):
		return nil
	}
}
