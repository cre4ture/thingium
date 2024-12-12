package fshigh

import (
	"errors"

	"github.com/syncthing/syncthing/lib/fs"
)

type HashBlockFilesystem interface {
	CommonFilesystemHL
	Open(name string) (HashBlockFile, error)
	OpenFile(name string, flags int, mode fs.FileMode) (HashBlockFile, error)
}

type HashBlockFile interface {
	fs.FileCommon
	WriteBlock(idx uint64, data []byte, blockSize uint64) error
}

type HashBlockFileToFileAdapter struct {
	fs.FileCommon
	dst fs.File
}

var _ = (HashBlockFile)((*HashBlockFileToFileAdapter)(nil))

func NewHashBlockFileToFileAdapter(dst fs.File) *HashBlockFileToFileAdapter {
	return &HashBlockFileToFileAdapter{
		FileCommon: dst,
		dst:        dst,
	}
}

// WriteBlock implements HashBlockFile.
func (h *HashBlockFileToFileAdapter) WriteBlock(idx uint64, data []byte, blockSize uint64) error {
	if len(data) > int(blockSize) {
		return errors.New("HashBlockFileToFileAdapter::WriteBlock(): disallowed block size")
	}
	offset := idx * blockSize
	written := 0
	for {
		n, err := h.dst.WriteAt(data, int64(offset))
		if err != nil {
			return err
		}
		written += n
		if written >= len(data) {
			return nil
		}
	}
}

type HashBlockFilesystemAdapter struct {
	CommonFilesystemHL
	classicFs *ClassicFilesystemHL
}

var _ = (HashBlockFilesystem)((*HashBlockFilesystemAdapter)(nil))

func NewHashBlockFilesystemAdapter(classicFs *ClassicFilesystemHL) *HashBlockFilesystemAdapter {
	return &HashBlockFilesystemAdapter{
		CommonFilesystemHL: classicFs,
		classicFs:          classicFs,
	}
}

// Open implements HashBlockFilesystem.
func (h *HashBlockFilesystemAdapter) Open(name string) (HashBlockFile, error) {
	f, err := h.classicFs.Open(name)
	if err != nil {
		return nil, err
	}

	return NewHashBlockFileToFileAdapter(f), nil
}

// OpenFile implements HashBlockFilesystem.
func (h *HashBlockFilesystemAdapter) OpenFile(name string, flags int, mode fs.FileMode) (HashBlockFile, error) {
	f, err := h.classicFs.OpenFile(name, flags, mode)
	if err != nil {
		return nil, err
	}

	return NewHashBlockFileToFileAdapter(f), nil
}
