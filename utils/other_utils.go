package utils

// TODO (rafal): nothing

import (
	"crypto/rand"
	"github.com/AdguardTeam/golibs/log"
	"math/big"
	"strings"
	"unicode/utf8"
)

// GetRandomValue /**
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

// ShortText https://stackoverflow.com/questions/59955085/how-can-i-elliptically-truncate-text-in-golang
func ShortText(s string, maxLen int) string {
	if len(s) < maxLen {
		return s
	}

	if utf8.ValidString(s[:maxLen]) {
		return s[:maxLen]
	}
	return strings.ToValidUTF8(s[:maxLen+1], "")
}

func IsLocalHost(host string) bool {

	if strings.HasSuffix(host, ".") {
		host = host[:len(host)-1]
	}
	if len(strings.Split(host, ".")) <= 1 {
		return true
	}
	return false
}
