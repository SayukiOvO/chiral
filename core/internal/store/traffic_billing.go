package store

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
)

// billedTrafficBytes applies the displayed decimal rate and rounds upward
// once per credential delta. Rates currently use SQLite REAL / JSON numbers;
// their shortest round-trip decimal is the authoritative decimal value here.
// Multiplying in float64 would turn 100 * 1.1 into 110.00000000000001 and
// overcharge a byte; it also loses raw integer bytes above 2^53. Neither an
// epsilon nor a float64-derived rational can implement the decimal contract.
func billedTrafficBytes(up, down int64, rate float64) (int64, error) {
	if up < 0 || down < 0 || up > math.MaxInt64-down {
		return 0, fmt.Errorf("invalid traffic delta")
	}
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
		return 0, fmt.Errorf("invalid traffic billing rate")
	}
	total := up + down
	if total == 0 || rate == 0 {
		return 0, nil
	}
	if rate == 1 {
		return total, nil
	}
	ratio, ok := new(big.Rat).SetString(strconv.FormatFloat(rate, 'g', -1, 64))
	if !ok {
		return 0, fmt.Errorf("invalid traffic billing rate")
	}
	numerator := new(big.Int).Mul(big.NewInt(total), ratio.Num())
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, ratio.Denom(), remainder)
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return 0, fmt.Errorf("billed traffic exceeds supported range")
	}
	return quotient.Int64(), nil
}
