// Copyright (C) 2016 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package config

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
	case FolderTypeVirtual:
		return "virtual"
	default:
		return "unknown"
	}
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
	case "virtual":
		*t = FolderTypeVirtual
	default:
		*t = FolderTypeSendReceive
	}
	return nil
}
