package utils

// TODO (rafal): nothing

import (
	"crypto/rand"
	"github.com/AdguardTeam/golibs/log"
	"math/big"
	"strings"
	"unicode/utf8"
)

/**
 * GetRandomValue is a function that generates a random value within a specified
 * range. It takes two parameters, min and max, both of type int64, representing
 * the lower and upper bounds of the range, respectively. The function returns an
 * int64 value and an error.
 *
 * If the min and max values are equal, the function simply returns the min value
 * without generating a random number.
 *
 * Inside the function, a new big.Int value is created to represent the difference
 * between max and min. This is done using the SetInt64 method of the big package.
 *
 * Next, the rand.Int function is called with rand.Reader as the random number
 * generator and the big.Int value as the maximum value. This generates a random
 * number within the specified range.
 *
 * If an error occurs during the random number generation, it is logged using the
 * log.Error function.
 *
 * Finally, the generated random number is added to the min value and returned,
 * along with the error.
 */
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
