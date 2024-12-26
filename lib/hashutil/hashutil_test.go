package hashutil_test

import (
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/syncthing/syncthing/lib/hashutil"
)

var byteHash_1 [32]byte = sha256.Sum256([]byte("slhash_1"))
var byteHash_2 [32]byte = sha256.Sum256([]byte("slhash_2"))

func TestGenericHashSlasher_NoSlashes(t *testing.T) {
	noSlashes := hashutil.NewHashStringStrategy("")

	assert.Equal(t, "3efa957b72f257fc3a447aa656e35b817bfaca2e624f3e53369219cdd0440ef6", noSlashes.HashToSlashedStringMapKey(byteHash_1[:]))
	assert.Equal(t, "df48b13994eb58d1627a2a9564191e7219e5d95ebc7cd579a0f0ae9f54afecf1", noSlashes.HashToSlashedStringMapKey(byteHash_2[:]))
}

func TestGenericHashSlasher_s(t *testing.T) {
	noSlashes := hashutil.NewHashStringStrategy("s")

	assert.Equal(t, "3/e/f/a/9/5/7/b/7/2/f/2/5/7/f/c/3/a/4/4/7/a/a/6/5/6/e/3/5/b/8/1/7/b/f/a/c/a/2/e/6/2/4/f/3/e/5/3/3/6/9/2/1/9/c/d/d/0/4/4/0/e/f/6", noSlashes.HashToSlashedStringMapKey(byteHash_1[:]))
	assert.Equal(t, "d/f/4/8/b/1/3/9/9/4/e/b/5/8/d/1/6/2/7/a/2/a/9/5/6/4/1/9/1/e/7/2/1/9/e/5/d/9/5/e/b/c/7/c/d/5/7/9/a/0/f/0/a/e/9/f/5/4/a/f/e/c/f/1", noSlashes.HashToSlashedStringMapKey(byteHash_2[:]))
}

func TestGenericHashSlasher_s2x0(t *testing.T) {
	noSlashes := hashutil.NewHashStringStrategy("s2x0")

	assert.Equal(t, "3e/fa957b72f257fc3a447aa656e35b817bfaca2e624f3e53369219cdd0440ef6", noSlashes.HashToSlashedStringMapKey(byteHash_1[:]))
	assert.Equal(t, "df/48b13994eb58d1627a2a9564191e7219e5d95ebc7cd579a0f0ae9f54afecf1", noSlashes.HashToSlashedStringMapKey(byteHash_2[:]))
}

func TestGenericHashSlasher_s2x16(t *testing.T) {
	noSlashes := hashutil.NewHashStringStrategy("s2x16")

	assert.Equal(t, "3e/fa957b72f257fc3a/447aa656e35b817b/faca2e624f3e5336/9219cdd0440ef6", noSlashes.HashToSlashedStringMapKey(byteHash_1[:]))
	assert.Equal(t, "df/48b13994eb58d162/7a2a9564191e7219/e5d95ebc7cd579a0/f0ae9f54afecf1", noSlashes.HashToSlashedStringMapKey(byteHash_2[:]))
}
