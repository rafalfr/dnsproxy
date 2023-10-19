package utils

// TODO (rafalfr): nothing

import (
	"crypto/rand"
	"github.com/AdguardTeam/golibs/log"
	"math/big"
)

func GetRandomValue(min int64, max int64) (int64, error) {

	if min == max {
		return min, nil
	}

	b := new(big.Int).SetInt64(max - min)

	i, err := rand.Int(rand.Reader, b)
	if err != nil {
		log.Error("Can't generate random value: %v, %v", i, err)
	}

	return i.Int64() + min, err
}
