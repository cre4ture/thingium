package hashutil

import "encoding/hex"

func HashToStringMapKey(hash []byte) string {
	return hex.EncodeToString(hash)
}

func StringMapKeyToHash(str string) ([]byte, error) {
	return hex.DecodeString(str)
}
