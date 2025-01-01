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
	"path/filepath"
	"testing"

	archiver "github.com/restic/restic/lib/archiver"
	"github.com/stretchr/testify/assert"
	blobfilefs "github.com/syncthing/syncthing/lib/blob_file_fs"
	"github.com/syncthing/syncthing/lib/model"
	"github.com/syncthing/syncthing/lib/protocol"
)

func TestResticAdapter_cleanFilePathSplit(t *testing.T) {
	var path, name string
	path, name = filepath.Split("/a/b/c")
	assert.Equal(t, "/a/b/", path)
	assert.Equal(t, "c", name)

	path, name = filepath.Split("/a")
	assert.Equal(t, "/", path)
	assert.Equal(t, "a", name)

	path, name = filepath.Split("/")
	assert.Equal(t, "/", path)
	assert.Equal(t, "", name)

}

func TestResticAdapter_NoErrWhenRepoDoesntExist(t *testing.T) {

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
	assert.NoError(t, err)
	assert.NotNil(t, restic)
	restic.Close()
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
	return CreateRepoForTest2(t, tmpDir, init, "test-folder")
}

func CreateRepoForTest2(t *testing.T, tmpDir string, init bool, folder_name string) *blobfilefs.ResticAdapter {
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

	restic, err := blobfilefs.NewResticAdapter("test-host", folder_name, gopts)
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

func generateTestFile(name string, rgen *rand.Rand) (*protocol.FileInfo, [][]byte) {
	fullBlocks := 5
	lastBlockSize := 500
	blocks := fullBlocks + 1
	fi := &protocol.FileInfo{
		Name:         name,
		Size:         protocol.MinBlockSize*int64(fullBlocks) + int64(lastBlockSize),
		RawBlockSize: protocol.MinBlockSize,
	}

	fi.Blocks = make([]protocol.BlockInfo, blocks)
	blockData := make([][]byte, len(fi.Blocks))

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

	fi, blockData := generateTestFile("/test_file", rand.New(rand.NewSource(10)))

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

	fi, blockData := generateTestFile("/test_file", rand.New(rand.NewSource(10)))

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

func TestResticAdapter_WriteFileReadFileData_nestedFiles(t *testing.T) {

	tmpDir := t.TempDir()

	rgen := rand.New(rand.NewSource(10))
	fileCount := 8
	fis := make([]*protocol.FileInfo, fileCount)
	blockDatas := make([][][]byte, fileCount)
	i := 0
	fis[i], blockDatas[i] = generateTestFile("/test_file1", rgen)
	i += 1
	fis[i], blockDatas[i] = generateTestFile("/dir1/test_file2", rgen)
	i += 1
	fis[i], blockDatas[i] = generateTestFile("/dir1/test_file3", rgen)
	i += 1
	fis[i], blockDatas[i] = generateTestFile("/dir2/test_file4", rgen)
	i += 1
	fis[i], blockDatas[i] = generateTestFile("/dir2/dirA/test_file5", rgen)
	i += 1
	fis[i], blockDatas[i] = generateTestFile("/dir2/dirA/test_file6", rgen)
	i += 1
	fis[i], blockDatas[i] = generateTestFile("/test_file7", rgen)
	i += 1
	fis[i], blockDatas[i] = generateTestFile("/dir1/test_file8", rgen)
	i += 1

	func() {
		resticRepo := CreateRepoForTest(t, tmpDir, true)
		defer resticRepo.Close()

		for i := 0; i < fileCount; i++ {
			assert.Nil(t, resticRepo.UpdateFile(
				context.Background(),
				fis[i], func(block protocol.BlockInfo, status model.GetBlockDataResult) {
					// blockStatusCb
				}, func(block protocol.BlockInfo) ([]byte, error) {
					// downloadBlockDataCb
					bIdx := block.Offset / int64(fis[i].BlockSize())
					return blockDatas[i][bIdx], nil
				}))
		}
	}()

	func() {
		resticRepo := CreateRepoForTest(t, tmpDir, false)
		defer resticRepo.Close()

		for i := 0; i < fileCount; i++ {
			for bIdx := 0; bIdx < len(blockDatas[i]); bIdx++ {
				expectedHash := sha256.Sum256(blockDatas[i][bIdx])
				buf := make([]byte, fis[i].BlockSize())
				dataLen, err := resticRepo.GetHashBlockData(context.Background(), expectedHash[:], buf)
				assert.NoError(t, err)
				assert.Equal(t, len(blockDatas[i][bIdx]), dataLen)
				assert.Equal(t, blockDatas[i][bIdx], buf[:dataLen])
			}
		}
	}()

	func() {
		resticRepo := CreateRepoForTest(t, tmpDir, false)
		defer resticRepo.Close()

		for i := 0; i < fileCount; i++ {
			data, err := resticRepo.ReadFileData(context.Background(), fis[i].Name)
			assert.NoErrorf(t, err, "file: %v", fis[i].Name)
			assert.Equalf(t, fis[i].Size, int64(len(data)), "file: %v", fis[i].Name)
			for bIdx := 0; bIdx < len(blockDatas[i]); bIdx++ {
				blockEnd := (bIdx + 1) * fis[i].BlockSize()
				if blockEnd > int(fis[i].Size) {
					blockEnd = int(fis[i].Size)
				}
				assert.Equalf(t,
					blockDatas[i][bIdx],
					data[bIdx*fis[i].BlockSize():blockEnd],
					"file: %v, block: %v", fis[i].Name, bIdx,
				)
			}
		}
	}()
}

func TestResticAdapter_WriteFileReadFileDataOnlyOneSnapshot_nestedFiles(t *testing.T) {

	tmpDir := t.TempDir()

	rgen := rand.New(rand.NewSource(10))
	fileCount := 8
	fis := make([]*protocol.FileInfo, fileCount)
	blockDatas := make([][][]byte, fileCount)
	i := 0
	fis[i], blockDatas[i] = generateTestFile("/test_file1", rgen)
	i += 1
	fis[i], blockDatas[i] = generateTestFile("/dir1/test_file2", rgen)
	i += 1
	fis[i], blockDatas[i] = generateTestFile("/dir1/test_file3", rgen)
	i += 1
	fis[i], blockDatas[i] = generateTestFile("/dir2/test_file4", rgen)
	i += 1
	fis[i], blockDatas[i] = generateTestFile("/dir2/dirA/test_file5", rgen)
	i += 1
	fis[i], blockDatas[i] = generateTestFile("/dir2/dirA/test_file6", rgen)
	i += 1
	fis[i], blockDatas[i] = generateTestFile("/test_file7", rgen)
	i += 1
	fis[i], blockDatas[i] = generateTestFile("/dir1/test_file8", rgen)
	i += 1

	func() {
		resticRepo := CreateRepoForTest(t, tmpDir, true)
		defer resticRepo.Close()
		writer, err := resticRepo.StartScanOrPull(context.Background(), model.PullOptions{OnlyMissing: false, OnlyCheck: false, CheckData: false}, func() {})
		assert.NoError(t, err)
		defer writer.Finish(context.Background())

		for i := 0; i < fileCount; i++ {
			assert.Nil(t, writer.PullOne(
				context.Background(),
				fis[i],
				func(block protocol.BlockInfo, status model.GetBlockDataResult) {
					// blockStatusCb
				}, func(block protocol.BlockInfo) ([]byte, error) {
					// downloadBlockDataCb
					bIdx := block.Offset / int64(fis[i].BlockSize())
					return blockDatas[i][bIdx], nil
				}))
		}
	}()

	func() {
		resticRepo := CreateRepoForTest(t, tmpDir, false)
		defer resticRepo.Close()

		for i := 0; i < fileCount; i++ {
			for bIdx := 0; bIdx < len(blockDatas[i]); bIdx++ {
				expectedHash := sha256.Sum256(blockDatas[i][bIdx])
				buf := make([]byte, fis[i].BlockSize())
				dataLen, err := resticRepo.GetHashBlockData(context.Background(), expectedHash[:], buf)
				assert.NoError(t, err)
				assert.Equal(t, len(blockDatas[i][bIdx]), dataLen)
				assert.Equal(t, blockDatas[i][bIdx], buf[:dataLen])
			}
		}
	}()

	func() {
		resticRepo := CreateRepoForTest(t, tmpDir, false)
		defer resticRepo.Close()

		for i := 0; i < fileCount; i++ {
			data, err := resticRepo.ReadFileData(context.Background(), fis[i].Name)
			assert.NoErrorf(t, err, "file: %v", fis[i].Name)
			assert.Equalf(t, fis[i].Size, int64(len(data)), "file: %v", fis[i].Name)
			for bIdx := 0; bIdx < len(blockDatas[i]); bIdx++ {
				blockEnd := (bIdx + 1) * fis[i].BlockSize()
				if blockEnd > int(fis[i].Size) {
					blockEnd = int(fis[i].Size)
				}
				assert.Equalf(t,
					blockDatas[i][bIdx],
					data[bIdx*fis[i].BlockSize():blockEnd],
					"file: %v, block: %v", fis[i].Name, bIdx,
				)
			}
		}
	}()
}

func TestResticAdapter_WriteFileReadBlockDataByHashAfterWritingEncryptionToken_twoDifferenfFoldersInterleaved(t *testing.T) {

	tmpDir := t.TempDir()

	randg := rand.New(rand.NewSource(10))
	fi1, blockData1 := generateTestFile("/test_file", randg)
	fi2, blockData2 := generateTestFile("/test_file", randg)

	func() {
		resticRepo := CreateRepoForTest2(t, tmpDir, true, "test-folder1")
		defer resticRepo.Close()

		existingToken, err := resticRepo.GetEncryptionToken()
		assert.NotNil(t, err)
		assert.Nil(t, existingToken)
		assert.Nil(t, resticRepo.SetEncryptionToken([]byte("test_token1")))
	}()

	func() {
		resticRepo := CreateRepoForTest2(t, tmpDir, false, "test-folder2")
		defer resticRepo.Close()

		existingToken, err := resticRepo.GetEncryptionToken()
		assert.NotNil(t, err)
		assert.Nil(t, existingToken)
		assert.Nil(t, resticRepo.SetEncryptionToken([]byte("test_token2")))
	}()

	func() {
		resticRepo := CreateRepoForTest2(t, tmpDir, false, "test-folder1")
		defer resticRepo.Close()

		assert.Nil(t, resticRepo.UpdateFile(
			context.Background(),
			fi1, func(block protocol.BlockInfo, status model.GetBlockDataResult) {
				// blockStatusCb
			}, func(block protocol.BlockInfo) ([]byte, error) {
				// downloadBlockDataCb
				bIdx := block.Offset / int64(fi1.BlockSize())
				return blockData1[bIdx], nil
			}))
	}()

	func() {
		resticRepo := CreateRepoForTest2(t, tmpDir, false, "test-folder2")
		defer resticRepo.Close()

		assert.Nil(t, resticRepo.UpdateFile(
			context.Background(),
			fi2, func(block protocol.BlockInfo, status model.GetBlockDataResult) {
				// blockStatusCb
			}, func(block protocol.BlockInfo) ([]byte, error) {
				// downloadBlockDataCb
				bIdx := block.Offset / int64(fi2.BlockSize())
				return blockData2[bIdx], nil
			}))
	}()

	func() {
		// data blocks from both file should be available in the repo
		resticRepo := CreateRepoForTest2(t, tmpDir, false, "test-folder1")
		defer resticRepo.Close()

		for bIdx := 0; bIdx < len(blockData1); bIdx++ {
			expectedHash := sha256.Sum256(blockData1[bIdx])
			buf := make([]byte, fi1.BlockSize())
			dataLen, err := resticRepo.GetHashBlockData(context.Background(), expectedHash[:], buf)
			assert.NoError(t, err)
			assert.Equal(t, len(blockData1[bIdx]), dataLen)
			assert.Equal(t, blockData1[bIdx], buf[:dataLen])
		}

		for bIdx := 0; bIdx < len(blockData2); bIdx++ {
			expectedHash := sha256.Sum256(blockData2[bIdx])
			buf := make([]byte, fi1.BlockSize())
			dataLen, err := resticRepo.GetHashBlockData(context.Background(), expectedHash[:], buf)
			assert.NoError(t, err)
			assert.Equal(t, len(blockData2[bIdx]), dataLen)
			assert.Equal(t, blockData2[bIdx], buf[:dataLen])
		}
	}()

	func() {
		// data blocks from both file should be available in the repo
		resticRepo := CreateRepoForTest2(t, tmpDir, false, "test-folder2")
		defer resticRepo.Close()

		for bIdx := 0; bIdx < len(blockData1); bIdx++ {
			expectedHash := sha256.Sum256(blockData1[bIdx])
			buf := make([]byte, fi1.BlockSize())
			dataLen, err := resticRepo.GetHashBlockData(context.Background(), expectedHash[:], buf)
			assert.NoError(t, err)
			assert.Equal(t, len(blockData1[bIdx]), dataLen)
			assert.Equal(t, blockData1[bIdx], buf[:dataLen])
		}

		for bIdx := 0; bIdx < len(blockData2); bIdx++ {
			expectedHash := sha256.Sum256(blockData2[bIdx])
			buf := make([]byte, fi1.BlockSize())
			dataLen, err := resticRepo.GetHashBlockData(context.Background(), expectedHash[:], buf)
			assert.NoError(t, err)
			assert.Equal(t, len(blockData2[bIdx]), dataLen)
			assert.Equal(t, blockData2[bIdx], buf[:dataLen])
		}
	}()

	func() {
		resticRepo := CreateRepoForTest2(t, tmpDir, false, "test-folder1")
		defer resticRepo.Close()

		data, err := resticRepo.GetEncryptionToken()
		assert.NoError(t, err)
		assert.Equal(t, []byte("test_token1"), data)
	}()

	func() {
		resticRepo := CreateRepoForTest2(t, tmpDir, false, "test-folder2")
		defer resticRepo.Close()

		data, err := resticRepo.GetEncryptionToken()
		assert.NoError(t, err)
		assert.Equal(t, []byte("test_token2"), data)
	}()
}
