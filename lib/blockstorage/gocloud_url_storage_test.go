package blockstorage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHashBlockStorageMapBuilder_addData(t *testing.T) {
	calls := 0
	lastState := HBS_NOT_AVAILABLE
	builder := NewHashBlockStorageMapBuilder(func(hash string, state HashBlockState) {
		calls += 1
		lastState = state
	})

	builder.addData("hash_1")
	builder.addData("hash_2")

	assert.Equal(t, 1, calls)
	assert.Equal(t, HBS_AVAILABLE, lastState)
}

func TestHashBlockStorageMapBuilder_addUse(t *testing.T) {
	calls := 0
	lastState := HBS_NOT_AVAILABLE
	builder := NewHashBlockStorageMapBuilder(func(hash string, state HashBlockState) {
		calls += 1
		lastState = state
	})

	builder.addData("hash_1")
	builder.addUse("hash_1")
	builder.addData("hash_2")

	assert.Equal(t, 1, calls)
	assert.Equal(t, HBS_AVAILABLE_HOLD, lastState)
}

func TestHashBlockStorageMapBuilder_addDelete(t *testing.T) {
	calls := 0
	lastState := HBS_NOT_AVAILABLE
	builder := NewHashBlockStorageMapBuilder(func(hash string, state HashBlockState) {
		calls += 1
		lastState = state
	})

	builder.addData("hash_1")
	builder.addDelete("hash_1")
	builder.addData("hash_2")

	assert.Equal(t, 0, calls)
	assert.Equal(t, HBS_NOT_AVAILABLE, lastState)
}
