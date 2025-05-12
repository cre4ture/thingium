package blockstorage

import (
	"context"
	"strings"

	"github.com/syncthing/syncthing/lib/model"
)

// configStr:
// 	- ":virtual:"
// 	- ":virtual-xxx:"
// 	- ":virtual-leveldb:"

func NewGoCloudUrlStorageFromConfigStr(ctx context.Context, configStr string, myDeviceId string) model.HashBlockStorageI {
	if res, ok := strings.CutPrefix(configStr, ":virtual-leveldb:"); ok {
		return NewBsLevelDB(res)
	} else {
		return NewGoCloudUrlStorageFromConfigStrConcrete(ctx, configStr, myDeviceId)
	}
}
