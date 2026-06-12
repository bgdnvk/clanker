package secrand

import (
	"crypto/rand"
	"math/big"
	"time"
)

// Duration returns a crypto-random duration in [0, maxExclusive). It returns
// zero if maxExclusive is not positive or the OS random source fails.
func Duration(maxExclusive time.Duration) time.Duration {
	if maxExclusive <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(maxExclusive)))
	if err != nil {
		return 0
	}
	return time.Duration(n.Int64())
}
