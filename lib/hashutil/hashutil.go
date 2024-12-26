package hashutil

import (
	"encoding/hex"
	"fmt"
	"math"
)

func HashToStringMapKey(hash []byte) string {
	return hex.EncodeToString(hash)
}

func StringMapKeyToHash(str string) ([]byte, error) {
	return hex.DecodeString(str)
}

func StringMapKeyToHashNoError(str string) []byte {
	hash, err := StringMapKeyToHash(str)
	if err != nil {
		panic(fmt.Sprintf("internal error: hash string %v doesn't convert to hash", str))
	}
	return hash
}

type HashStringStrategy struct {
	step1 uint
	stepX uint
}

func NewHashStringStrategy(strategy string) *HashStringStrategy {
	switch strategy {
	default:
		panic("hash string strategy not supported")
	case "":
		return &HashStringStrategy{
			step1: math.MaxUint, // disabled
			stepX: math.MaxUint, // disabled
		}
	case "s":
		return &HashStringStrategy{
			step1: 1,
			stepX: 1,
		}
	case "s2x0":
		return &HashStringStrategy{
			step1: 2,
			stepX: math.MaxUint, // disabled
		}
	case "s3x0":
		return &HashStringStrategy{
			step1: 3,
			stepX: math.MaxUint, // disabled
		}
	case "s2x16":
		return &HashStringStrategy{
			step1: 2,
			stepX: 16,
		}
	}
}

func (s *HashStringStrategy) HashToSlashedStringMapKey(hash []byte) string {
	hashString := HashToStringMapKey(hash)
	step1 := int(math.Min(float64(len(hashString)), float64(s.step1)))

	slashed := hashString[:step1] + "/"
	hashString = hashString[step1:]

	stepX := int(math.Min(float64(len(hashString)), float64(s.stepX)))
	for stepX < len(hashString) {
		slashed += hashString[:stepX] + "/"
		hashString = hashString[stepX:]
	}

	if len(hashString) > 0 {
		slashed += hashString + "/"
	}

	return slashed[:len(slashed)-1]
}

func (s *HashStringStrategy) SlashedStringMapKeyToHashNoError(str string) []byte {
	step1 := int(math.Min(float64(len(str)), float64(s.step1)))
	unslashed := str[0:step1]
	remaining := str[step1:]

	stepX := int(math.Min(float64(len(str)), float64(s.stepX)))
	for i := 0; i < len(remaining); i += stepX + 1 {
		if remaining[i] != '/' {
			panic(fmt.Sprintf("internal error: slashed hash string %v doesn't convert to hash. Its missing a / at position %v", remaining, i))
		}
		unslashed += remaining[i+1 : i+1+stepX]
	}
	return StringMapKeyToHashNoError(unslashed)
}
