package blockstorage

import (
	"crypto/sha256"

	"github.com/syncthing/syncthing/lib/hashutil"
)

// additionally calculates and stores real hash of encrypted data.
// this enables the detection of bit-rot
type EncryptedHashBlockStorage struct {
	store HashBlockStorageI
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
func (e *EncryptedHashBlockStorage) Delete(hash []byte) {
	e.store.Delete(hash)
	e.store.DeleteMeta(e.genRealHashKey(hash))
}

// DeleteMeta implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) DeleteMeta(name string) {
	e.store.DeleteMeta(name)
}

// Get implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) Get(hash []byte) (data []byte, ok bool) {
	return e.store.Get(hash)
}

// GetBlockHashesCache implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) GetBlockHashesCache(progressNotifier func(int)) map[string]struct{} {
	return e.store.GetBlockHashesCache(progressNotifier)
}

// GetBlockHashesCountHint implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) GetBlockHashesCountHint() int {
	return e.store.GetBlockHashesCountHint()
}

// GetMeta implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) GetMeta(name string) (data []byte, ok bool) {
	return e.store.GetMeta(name)
}

// Has implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) Has(hash []byte) (ok bool) {
	return e.store.Has(hash)
}

// IterateBlocks implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) IterateBlocks(fn func(hash []byte) bool) error {
	return e.store.IterateBlocks(fn)
}

// Set implements HashBlockStorageI.
func (e *EncryptedHashBlockStorage) Set(hash []byte, data []byte) {
	real_hash := sha256.Sum256(data)
	e.store.SetMeta(e.genRealHashKey(hash), real_hash[:])
	e.store.Set(hash, data)
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
