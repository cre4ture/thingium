// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package blobfilefs_test

import (
	"context"
	"crypto/sha256"
	"math/rand"
	"os"
	"testing"

	archiver "github.com/restic/restic/lib/archiver"
	"github.com/stretchr/testify/assert"
	blobfilefs "github.com/syncthing/syncthing/lib/blob_file_fs"
	"github.com/syncthing/syncthing/lib/model"
	"github.com/syncthing/syncthing/lib/protocol"
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

func generateTestFile() (*protocol.FileInfo, [][]byte) {
	fullBlocks := 5
	lastBlockSize := 500
	blocks := fullBlocks + 1
	fi := &protocol.FileInfo{
		Name:         "test_file",
		Size:         protocol.MinBlockSize*int64(fullBlocks) + int64(lastBlockSize),
		RawBlockSize: protocol.MinBlockSize,
	}

	fi.Blocks = make([]protocol.BlockInfo, blocks)
	blockData := make([][]byte, len(fi.Blocks))

	rgen := rand.New(rand.NewSource(10))
	for bIdx := 0; bIdx < fullBlocks; bIdx++ {
		for di := 0; di < protocol.MinBlockSize; di++ {
			blockData[bIdx] = append(blockData[bIdx], byte(rgen.Intn(256)))
		}
	}
	for di := 0; di < lastBlockSize; di++ {
		blockData[fullBlocks] = append(blockData[fullBlocks], byte(rgen.Intn(256)))
	}

	for bIdx := 0; bIdx < blocks; bIdx++ {
		hash := sha256.Sum256(blockData[bIdx])
		fi.Blocks[bIdx] = protocol.BlockInfo{
			Offset: int64(bIdx) * int64(fi.BlockSize()),
			Size:   len(blockData[bIdx]),
			Hash:   hash[:],
		}
	}

	return fi, blockData
}

func TestResticAdapter_WriteFileReadBlockDataByHash(t *testing.T) {

	tmpDir := t.TempDir()

	fi, blockData := generateTestFile()

	func() {
		resticRepo := CreateRepoForTest(t, tmpDir, true)
		defer resticRepo.Close()

		assert.Nil(t, resticRepo.UpdateFile(
			context.Background(),
			fi, func(block protocol.BlockInfo, status model.GetBlockDataResult) {
				// blockStatusCb
			}, func(block protocol.BlockInfo) ([]byte, error) {
				// downloadBlockDataCb
				bIdx := block.Offset / int64(fi.BlockSize())
				return blockData[bIdx], nil
			}))
	}()

	func() {
		resticRepo := CreateRepoForTest(t, tmpDir, false)
		defer resticRepo.Close()

		for bIdx := 0; bIdx < len(blockData); bIdx++ {
			expectedHash := sha256.Sum256(blockData[bIdx])
			buf := make([]byte, fi.BlockSize())
			dataLen, err := resticRepo.GetHashBlockData(context.Background(), expectedHash[:], buf)
			assert.NoError(t, err)
			assert.Equal(t, len(blockData[bIdx]), dataLen)
			assert.Equal(t, blockData[bIdx], buf[:dataLen])
		}
	}()
}

func TestResticAdapter_WriteFileReadBlockDataByHashAfterWritingEncryptionToken(t *testing.T) {

	tmpDir := t.TempDir()

	fi, blockData := generateTestFile()

	func() {
		resticRepo := CreateRepoForTest(t, tmpDir, true)
		defer resticRepo.Close()

		assert.Nil(t, resticRepo.SetEncryptionToken([]byte("test_token")))
	}()

	func() {
		resticRepo := CreateRepoForTest(t, tmpDir, false)
		defer resticRepo.Close()

		assert.Nil(t, resticRepo.UpdateFile(
			context.Background(),
			fi, func(block protocol.BlockInfo, status model.GetBlockDataResult) {
				// blockStatusCb
			}, func(block protocol.BlockInfo) ([]byte, error) {
				// downloadBlockDataCb
				bIdx := block.Offset / int64(fi.BlockSize())
				return blockData[bIdx], nil
			}))
	}()

	func() {
		resticRepo := CreateRepoForTest(t, tmpDir, false)
		defer resticRepo.Close()

		for bIdx := 0; bIdx < len(blockData); bIdx++ {
			expectedHash := sha256.Sum256(blockData[bIdx])
			buf := make([]byte, fi.BlockSize())
			dataLen, err := resticRepo.GetHashBlockData(context.Background(), expectedHash[:], buf)
			assert.NoError(t, err)
			assert.Equal(t, len(blockData[bIdx]), dataLen)
			assert.Equal(t, blockData[bIdx], buf[:dataLen])
		}
	}()

	func() {
		resticRepo := CreateRepoForTest(t, tmpDir, false)
		defer resticRepo.Close()

		data, err := resticRepo.GetEncryptionToken()
		assert.NoError(t, err)
		assert.Equal(t, []byte("test_token"), data)
	}()

}
