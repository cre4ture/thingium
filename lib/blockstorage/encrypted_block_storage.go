package blockstorage

import (
	"context"
	"crypto/sha256"

	"github.com/syncthing/syncthing/lib/hashutil"
)

// additionally calculates and stores real hash of encrypted data.
// this enables the detection of bit-rot
type EncryptedHashBlockStorage struct {
	store HashBlockStorageI
}

// AnnounceDelete implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) AnnounceDelete(hash []byte) error {
	return e.store.AnnounceDelete(hash)
}

// DeAnnounceDelete implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) DeAnnounceDelete(hash []byte) error {
	return e.store.DeAnnounceDelete(hash)
}

// GetBlockHashState implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) GetBlockHashState(hash []byte) HashBlockState {
	return e.store.GetBlockHashState(hash)
}

// UncheckedDelete implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) UncheckedDelete(hash []byte) error {
	return e.store.UncheckedDelete(hash)
}

const HASH_MAPPING_PREFIX = "real_hashes/"

func (e *EncryptedHashBlockStorage) genRealHashKey(hash []byte) string {
	return HASH_MAPPING_PREFIX + hashutil.HashToStringMapKey(hash)
}

// Close implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) Close() error {
	return e.store.Close()
}

// Delete implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) DeleteReservation(hash []byte) {
	e.store.DeleteReservation(hash)
	// TODO: how to cleanup related metadata?
	//e.store.DeleteMeta(e.genRealHashKey(hash))
}

// DeleteMeta implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) DeleteMeta(name string) {
	e.store.DeleteMeta(name)
}

// Get implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) ReserveAndGet(hash []byte, downloadData bool) (data []byte, ok bool) {
	return e.store.ReserveAndGet(hash, downloadData)
}

// GetBlockHashesCache implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) GetBlockHashesCache(
	ctx context.Context, progressNotifier func(count int, currentHash []byte)) HashBlockStateMap {
	return e.store.GetBlockHashesCache(ctx, progressNotifier)
}

// GetBlockHashesCountHint implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) GetBlockHashesCountHint() int {
	return e.store.GetBlockHashesCountHint()
}

// GetMeta implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) GetMeta(name string) (data []byte, ok bool) {
	return e.store.GetMeta(name)
}

//// Has implements HashBlockStorageI.
//func (e *EncryptedHashBlockStorage) Has(hash []byte) (ok bool) {
//	return e.store.Has(hash)
//}

//// IterateBlocks implements HashBlockStorageI.
//func (e *EncryptedHashBlockStorage) IterateBlocks(fn func(hash []byte, state HashBlockState) bool) error {
//	return e.store.IterateBlocks(fn)
//}

// Set implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) ReserveAndSet(hash []byte, data []byte) {
	real_hash := sha256.Sum256(data)
	e.store.SetMeta(e.genRealHashKey(hash), real_hash[:])
	e.store.ReserveAndSet(hash, data)
}

// SetMeta implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) SetMeta(name string, data []byte) {
	e.store.SetMeta(name, data)
}

func NewEncryptedHashBlockStorage(store HashBlockStorageI) *EncryptedHashBlockStorage {
	return &EncryptedHashBlockStorage{
		store: store,
	}
}
