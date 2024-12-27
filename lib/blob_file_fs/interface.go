// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package blobfilefs

import "github.com/syncthing/syncthing/lib/protocol"

type BlobFsI interface {
	UpdateFile(fi *protocol.FileInfo, downloadCb func(hash []byte) ([]byte, error)) error
}
