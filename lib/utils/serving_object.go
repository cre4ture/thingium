// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package utils

import (
	"context"
)

type ServingObject[T any] struct {
	serviceWorkGroup *SafeWorkGroup
	normalWorkGroup  *SafeWorkGroup
	obj              *T
}

func NewServingObject[T any](workCtx context.Context, obj *T, maxParallel int) *ServingObject[T] {
	return &ServingObject[T]{
		serviceWorkGroup: NewSafeWorkGroup(context.Background(), 0),
		normalWorkGroup:  NewSafeWorkGroup(workCtx, maxParallel),
		obj:              obj,
	}
}

func (so *ServingObject[T]) ServiceRoutineGo(fn func(obj *T, ctx context.Context)) {
	so.serviceWorkGroup.Go(func(ctx context.Context) {
		fn(so.obj, ctx)
	})
}

func (so *ServingObject[T]) ServiceRoutineRun(fn func(obj *T, done func(), ctx context.Context)) {
	so.serviceWorkGroup.Run(func(done func(), ctx context.Context) {
		fn(so.obj, done, ctx)
	})
}

func (so *ServingObject[T]) NormalWorkerRun(work func(obj *T, done func(), ctx context.Context)) {
	so.normalWorkGroup.Run(func(done func(), ctx context.Context) {
		work(so.obj, done, ctx)
	})
}

func (so *ServingObject[T]) Close() {
	so.normalWorkGroup.WaitNoClose()
	so.serviceWorkGroup.CloseAndWait()
}
