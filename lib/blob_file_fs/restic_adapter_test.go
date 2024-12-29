// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package blobfilefs_test

import (
	"context"
	"os"
	"testing"

	archiver "github.com/restic/restic/lib/archiver"
	"github.com/stretchr/testify/assert"
	blobfilefs "github.com/syncthing/syncthing/lib/blob_file_fs"
)

func TestResticAdapter_ErrWhenRepoDoesntExist(t *testing.T) {

	tmpDir := t.TempDir()
	repoDir := tmpDir + "/restic_repo"
	os.Mkdir(repoDir, 0755)

	gopts := archiver.GetDefaultEasyArchiverOptions()
	gopts.Repo = repoDir
	gopts.CacheDir = tmpDir + "/restic_cache"
	gopts.PasswordFile = tmpDir + "/restic_password"

	passwd := []byte("syncthing")
	err := os.WriteFile(gopts.PasswordFile, passwd, 0644)
	assert.NoError(t, err)

	restic, err := blobfilefs.NewResticAdapter("test-host", "test-folder", gopts)
	assert.ErrorIs(t, err, archiver.ErrNoRepository)
	assert.Nil(t, restic)
}

func TestResticAdapter_InitializationSuccessful(t *testing.T) {

	tmpDir := t.TempDir()
	repoDir := tmpDir + "/restic_repo"
	os.Mkdir(repoDir, 0755)

	gopts := archiver.GetDefaultEasyArchiverOptions()
	gopts.Repo = repoDir
	gopts.CacheDir = tmpDir + "/restic_cache"
	gopts.PasswordFile = tmpDir + "/restic_password"

	passwd := []byte("syncthing")
	err := os.WriteFile(gopts.PasswordFile, passwd, 0644)
	assert.NoError(t, err)

	iopts := archiver.EasyArchiverInitOptions{}

	err = archiver.InitNewRepository(context.Background(), iopts, gopts)
	assert.NoError(t, err)

	restic, err := blobfilefs.NewResticAdapter("test-host", "test-folder", gopts)
	assert.NoError(t, err, nil)
	assert.NotNil(t, restic)

	restic.Close()
}

func TestResticAdapter_InitializationSuccessfulFailsSecondTime(t *testing.T) {

	tmpDir := t.TempDir()
	repoDir := tmpDir + "/restic_repo"
	os.Mkdir(repoDir, 0755)

	gopts := archiver.GetDefaultEasyArchiverOptions()
	gopts.Repo = repoDir
	gopts.CacheDir = tmpDir + "/restic_cache"
	gopts.PasswordFile = tmpDir + "/restic_password"

	passwd := []byte("syncthing")
	err := os.WriteFile(gopts.PasswordFile, passwd, 0644)
	assert.NoError(t, err)

	iopts := archiver.EasyArchiverInitOptions{}

	err = archiver.InitNewRepository(context.Background(), iopts, gopts)
	assert.NoError(t, err)

	restic, err := blobfilefs.NewResticAdapter("test-host", "test-folder", gopts)
	assert.NoError(t, err, nil)
	assert.NotNil(t, restic)

	restic.Close()

	err = archiver.InitNewRepository(context.Background(), iopts, gopts)
	assert.NotNil(t, err)
}

func CreateRepoForTest(t *testing.T, tmpDir string, init bool) *blobfilefs.ResticAdapter {
	repoDir := tmpDir + "/restic_repo"
	os.Mkdir(repoDir, 0755)

	gopts := archiver.GetDefaultEasyArchiverOptions()
	gopts.Repo = repoDir
	gopts.CacheDir = tmpDir + "/restic_cache"
	gopts.PasswordFile = tmpDir + "/restic_password"

	passwd := []byte("syncthing")
	err := os.WriteFile(gopts.PasswordFile, passwd, 0644)
	assert.NoError(t, err)

	if init {
		iopts := archiver.EasyArchiverInitOptions{}
		err := archiver.InitNewRepository(context.Background(), iopts, gopts)
		assert.NoError(t, err)
	}

	restic, err := blobfilefs.NewResticAdapter("test-host", "test-folder", gopts)
	assert.NoError(t, err, nil)
	assert.NotNil(t, restic)

	return restic
}

func TestResticAdapter_WriteReadEncryptionToken(t *testing.T) {

	tmpDir := t.TempDir()

	func() {
		resticRepo := CreateRepoForTest(t, tmpDir, true)
		defer resticRepo.Close()

		assert.Nil(t, resticRepo.SetEncryptionToken([]byte("test_token")))
	}()

	func() {
		resticRepo := CreateRepoForTest(t, tmpDir, false)
		defer resticRepo.Close()

		data, err := resticRepo.GetEncryptionToken()
		assert.NoError(t, err)
		assert.Equal(t, []byte("test_token"), data)
	}()
}
