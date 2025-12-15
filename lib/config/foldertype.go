// Copyright (C) 2016 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package config

import "github.com/syncthing/syncthing/lib/protocol"

type FolderType protocol.FolderType

const (
	FolderTypeSendReceive      = FolderType(protocol.FolderTypeSendReceive)
	FolderTypeSendOnly         = FolderType(protocol.FolderTypeSendOnly)
	FolderTypeReceiveOnly      = FolderType(protocol.FolderTypeReceiveOnly)
	FolderTypeReceiveEncrypted = FolderType(protocol.FolderTypeReceiveEncrypted)
)

func (t FolderType) String() string {
	switch t {
	case FolderTypeSendReceive:
		return "sendreceive"
	case FolderTypeSendOnly:
		return "sendonly"
	case FolderTypeReceiveOnly:
		return "receiveonly"
	case FolderTypeReceiveEncrypted:
		return "receiveencrypted"
	default:
		return "unknown"
	}
}

func (t FolderType) SupportsIgnores() bool {
	// return t != FolderTypeReceiveEncrypted
	// To be able to use the sharding feature, ignores must be enabled
	// TODO: discuss with maintainer how this can be solved properly
	return true
}

func (t FolderType) MarshalText() ([]byte, error) {
	return []byte(t.String()), nil
}

func (t *FolderType) UnmarshalText(bs []byte) error {
	switch string(bs) {
	case "readwrite", "sendreceive":
		*t = FolderTypeSendReceive
	case "readonly", "sendonly":
		*t = FolderTypeSendOnly
	case "receiveonly":
		*t = FolderTypeReceiveOnly
	case "receiveencrypted":
		*t = FolderTypeReceiveEncrypted
	default:
		*t = FolderTypeSendReceive
	}
	return nil
}
