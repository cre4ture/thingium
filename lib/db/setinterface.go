// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import "github.com/syncthing/syncthing/lib/protocol"

type DbSnapshotI interface {
	Release()
	GetGlobal(file string) (protocol.FileInfo, bool)
	GetGlobalTruncated(file string) (protocol.FileInfo, bool)
	WithPrefixedGlobalTruncated(prefix string, fn Iterator)
}
