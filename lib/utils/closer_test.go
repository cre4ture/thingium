// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package utils_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syncthing/syncthing/lib/utils"
)

var (
	ErrTest1 = errors.New("test1")
	ErrTest2 = errors.New("test2")
)

type closingFunc struct {
	closable func() error
}

func (c closingFunc) Close() error {
	return c.closable()
}

func TestCloser_inverseClosingOrder(t *testing.T) {
	c := utils.NewCloser()
	iter := 1
	closed1 := 0
	closed2 := 0
	c.RegisterCleanupFunc(func() error {
		closed1 = iter
		iter++
		return nil
	})
	c.RegisterCloseable(&closingFunc{func() error {
		closed2 = iter
		iter++
		return nil
	}})

	require.NoError(t, c.Close())

	assert.Equal(t, 1, closed2)
	assert.Equal(t, 2, closed1)
}

func TestCloser_unregistering(t *testing.T) {
	c := utils.NewCloser()
	iter := 1
	closed1 := 0
	closed2 := 0
	c.RegisterCleanupFunc(func() error {
		closed1 = iter
		iter++
		return nil
	})
	closable2 := &closingFunc{func() error {
		closed2 = iter
		iter++
		return nil
	}}
	c.RegisterCloseable(closable2)

	c.UnregisterCloseable(closable2)

	require.NoError(t, c.Close())

	assert.Equal(t, 0, closed2)
	assert.Equal(t, 1, closed1)
}

func TestCloser_unregisterAll(t *testing.T) {
	c := utils.NewCloser()
	iter := 1
	closed1 := 0
	closed2 := 0
	c.RegisterCleanupFunc(func() error {
		closed1 = iter
		iter++
		return nil
	})
	closable2 := &closingFunc{func() error {
		closed2 = iter
		iter++
		return nil
	}}
	c.RegisterCloseable(closable2)

	c.UnregisterAll()

	require.NoError(t, c.Close())

	assert.Equal(t, 0, closed2)
	assert.Equal(t, 0, closed1)
}

func TestCloser_errorForwarding1(t *testing.T) {
	c := utils.NewCloser()
	c.RegisterCleanupFunc(func() error {
		return ErrTest1
	})
	c.RegisterCloseable(&closingFunc{func() error {
		return ErrTest2
	}})

	assert.ErrorIs(t, c.Close(), ErrTest2)
}

func TestCloser_errorForwarding2(t *testing.T) {
	c := utils.NewCloser()
	c.RegisterCleanupFunc(func() error {
		return ErrTest1
	})
	c.RegisterCloseable(&closingFunc{func() error {
		return nil
	}})

	assert.ErrorIs(t, c.Close(), ErrTest1)
}
