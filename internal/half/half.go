// Package half converts exact rational numbers to IEEE 754 binary16
// (half precision) bit patterns using only integer/rational arithmetic.
//
// binary16 layout: 1 sign bit, 5 exponent bits, 10 fraction bits,
// bias 15. Rounding uses roundTiesToEven ("nearest, ties to even").
package half

import (
	"math/big"
)

// Class is the IEEE category of a binary16 result.
type Class string

const (
	ClassZero      Class = "zero"
	ClassSubnormal Class = "subnormal"
	ClassNormal    Class = "normal"
	ClassInfinity  Class = "infinity"
)

// Bits is a complete binary16 conversion result.
type Bits struct {
	// Sign is the raw sign bit (0 positive, 1 negative).
	Sign uint16
	// E is the stored 5-bit exponent field (0 for zero/subnormal,
	// 31 for infinity).
	E uint16
	// F is the stored 10-bit fraction field.
	F uint16
	// Value is the exact rational represented by the bit pattern
	// (nil for infinity).
	Value *big.Rat
	Class Class
}

// Interval is an exact certificate of the signed input values that round
// to a binary16 bit pattern under roundTiesToEven.
//
// A nil bound means that side is unbounded (-∞ or +∞). The inclusive
// flags are meaningful only for the corresponding non-nil endpoint.
type Interval struct {
	Lower          *big.Rat
	Upper          *big.Rat
	LowerInclusive bool
	UpperInclusive bool
}

// Pattern returns the 16-bit encoding, formatted as four hex digits
// by callers.
func (b Bits) Pattern() uint16 {
	return (b.Sign << 15) | (b.E << 10) | b.F
}

// IsInf reports whether the result is an infinity.
func (b Bits) IsInf() bool { return b.Class == ClassInfinity }

// powersOfTwo caches 2^e for the small range visited during binade
// searches and interval certificates (-27..16).
var powersOfTwo = map[int]*big.Rat{}

// p2 returns the exact rational 2^e. The range needed is -27..16.
func p2(e int) *big.Rat {
	if v, ok := powersOfTwo[e]; ok {
		// Return a copy: callers never mutate cached rationals in
		// practice, but keep the map values shared-read only.
		return new(big.Rat).Set(v)
	}
	r := new(big.Rat)
	if e >= 0 {
		r.SetInt(new(big.Int).Lsh(big.NewInt(1), uint(e)))
	} else {
		r.SetFrac(big.NewInt(1), new(big.Int).Lsh(big.NewInt(1), uint(-e)))
	}
	powersOfTwo[e] = r
	return new(big.Rat).Set(r)
}

// roundScale rounds r * 2^s (r >= 0) to the nearest integer, with ties
// breaking toward the even integer. The result fits in int64 for every
// caller, but big.Int is used throughout to keep the arithmetic honest.
func roundScale(r *big.Rat, s int) *big.Int {
	z := new(big.Rat).Set(r)
	if s >= 0 {
		z.Mul(z, new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), uint(s))))
	} else {
		z.Quo(z, new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), uint(-s))))
	}

	num := z.Num()
	den := z.Denom()
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	twiceRem := new(big.Int).Lsh(rem, 1)
	cmp := twiceRem.Cmp(den)
	if cmp > 0 || (cmp == 0 && q.Bit(0) == 1) {
		q.Add(q, big.NewInt(1))
	}
	return q
}

func cmpRat(x *big.Rat, y *big.Rat) int { return x.Cmp(y) }

// Convert rounds the exact rational r to binary16 with roundTiesToEven.
// The neg argument carries the sign (r may be a negative rational, but
// Parse also reports the sign of an input zero).
func Convert(neg bool, r *big.Rat) Bits {
	zero := func(sign uint16) Bits {
		return Bits{Sign: sign, E: 0, F: 0, Value: new(big.Rat), Class: ClassZero}
	}
	if r.Sign() == 0 {
		var sign uint16
		if neg {
			sign = 1
		}
		return zero(sign)
	}

	neg = neg || r.Sign() < 0
	x := new(big.Rat).Abs(r)

	var sign uint16
	if neg {
		sign = 1
	}

	mkInf := func() Bits { return Bits{Sign: sign, E: 31, F: 0, Value: nil, Class: ClassInfinity} }

	// Largest finite is 0x7BFF = 65504; next binade midpoint is 65520.
	maxFinite := new(big.Rat).SetInt64(65504)
	infMidpoint := new(big.Rat).SetInt64(65520)

	var E, F uint16

	switch {
	case cmpRat(x, infMidpoint) >= 0:
		// x == 65520 is the exact tie between 65504 and infinity;
		// the infinity candidate has stored field 0 (even), so the
		// tie resolves to infinity.
		return mkInf()

	case cmpRat(x, maxFinite) >= 0:
		// [65504, 65520): strictly closer to 65504.
		E, F = 30, 1023

	case cmpRat(x, p2(-14)) < 0:
		// Below the smallest normal 2^-14: subnormal spacing is
		// 2^-24. k rounds to 0..1023, except that the tie between
		// k=1023 and k=1024 rounds even to 1024, which is exactly
		// the smallest normal.
		k := roundScale(x, 24)
		switch kv := k.Int64(); {
		case kv == 0:
			return zero(sign)
		case kv == 1024:
			E, F = 1, 0
		default:
			E, F = 0, uint16(kv)
		}

	default:
		// Normal region: 2^-14 <= x < 65520. Find the binade
		// [2^e, 2^(e+1)) containing x; an exact power of two is the
		// left endpoint and ties resolve to it via the even m.
		e := -14
		for cmpRat(x, p2(e+1)) >= 0 && e < 15 {
			e++
		}
		// m = x / 2^e * 1024, rounded ties to even.
		m := roundScale(x, 10-e)
		mi := uint16(m.Int64())
		if mi == 2048 {
			E, F = uint16(e+1+15), 0
		} else {
			E, F = uint16(e+15), mi-1024
		}
	}

	val := finiteMagnitude(E, F)
	if neg {
		val.Neg(val)
	}
	return Bits{Sign: sign, E: E, F: F, Value: val, Class: classOf(E, F)}
}

// finiteMagnitude returns the exact nonnegative rational encoded by the
// given stored fields.
func finiteMagnitude(E, F uint16) *big.Rat {
	if E == 0 {
		if F == 0 {
			return new(big.Rat)
		}
		// F * 2^-24
		return new(big.Rat).SetFrac(
			new(big.Int).SetInt64(int64(F)),
			new(big.Int).Lsh(big.NewInt(1), 24),
		)
	}
	// significand = 1024 + F, value = significand * 2^(E-25)
	sig := big.NewInt(int64(1024 + F))
	v := new(big.Rat).SetInt(sig)
	shift := int(E) - 25
	if shift >= 0 {
		v.Mul(v, new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), uint(shift))))
	} else {
		v.Quo(v, new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), uint(-shift))))
	}
	return v
}

func classOf(E, F uint16) Class {
	switch {
	case E == 0 && F == 0:
		return ClassZero
	case E == 0:
		return ClassSubnormal
	default:
		return ClassNormal
	}
}

// Error returns the exact rounding error value(result) - original,
// reduced to lowest terms. It is nil for infinite results.
func Error(b Bits, original *big.Rat) *big.Rat {
	if b.IsInf() {
		return nil
	}
	return new(big.Rat).Sub(b.Value, original)
}

// Certificate returns the exact set of signed finite decimal rationals
// that Convert rounds to b. The endpoint tie decisions follow
// roundTiesToEven, so they are not always closed: a midpoint belongs to
// the adjacent even candidate rather than both neighboring patterns.
func Certificate(b Bits) Interval {
	if b.IsInf() {
		threshold := new(big.Rat).SetInt64(65520)
		if b.Sign == 0 {
			return Interval{Lower: threshold, LowerInclusive: true}
		}
		threshold.Neg(threshold)
		return Interval{Upper: threshold, UpperInclusive: true}
	}

	v := new(big.Rat).Set(b.Value)

	if b.Class == ClassZero {
		endpoint := p2(-25) // |v|=2^-25 is the even tie with zero
		if b.Sign == 1 {
			endpoint.Neg(endpoint)
			return Interval{Lower: endpoint, Upper: v,
				LowerInclusive: true, UpperInclusive: true}
		}
		return Interval{Lower: v, Upper: endpoint,
			LowerInclusive: true, UpperInclusive: true}
	}

	mag := new(big.Rat).Abs(v)

	// halfGap returns half the distance to the neighboring candidate on
	// the requested side. At a normal power of two the lower neighbor is
	// in the previous binade, so its ULP can be smaller than the current
	// binade's ULP.
	halfGap := func(higher bool) *big.Rat {
		if b.E == 0 {
			return p2(-25)
		}
		if b.F == 0 && !higher {
			if b.E == 1 {
				// Lower neighbor is the largest subnormal, whose spacing
				// is 2^-24; its half is 2^-25 (the 2047/2^25 tie).
				return p2(-25)
			}
			if b.E > 1 {
				return p2(int(b.E) - 27)
			}
		}
		// The upper neighbor of the largest finite value is infinity,
		// and the overflow tie is at 65520 (half-gap 16 = 2^4).
		if b.E == 30 && b.F == 1023 && higher {
			return p2(4)
		}
		return p2(int(b.E) - 26)
	}

	lower := new(big.Rat).Sub(mag, halfGap(false))
	upper := new(big.Rat).Add(mag, halfGap(true))
	inclusive := b.F&1 == 0

	if b.Sign == 1 {
		positiveLower := new(big.Rat).Set(lower)
		lower.Neg(upper)
		upper.Neg(positiveLower)
	}
	return Interval{Lower: lower, Upper: upper,
		LowerInclusive: inclusive, UpperInclusive: inclusive}
}

// Contains reports whether a finite signed input with the supplied literal
// sign belongs to the interval. The neg flag is required to distinguish
// signed zero inputs, whose rationals compare equal.
func (in Interval) Contains(neg bool, r *big.Rat) bool {
	if r == nil {
		return false
	}
	if r.Sign() == 0 {
		return in.containsZero(neg)
	}
	if in.Lower != nil {
		if c := r.Cmp(in.Lower); c < 0 || (c == 0 && !in.LowerInclusive) {
			return false
		}
	}
	if in.Upper != nil {
		if c := r.Cmp(in.Upper); c > 0 || (c == 0 && !in.UpperInclusive) {
			return false
		}
	}
	return true
}

func (in Interval) containsZero(negativeLiteral bool) bool {
	zero := new(big.Rat)
	if in.Lower != nil && in.Lower.Cmp(zero) == 0 && in.Upper != nil && in.Upper.Sign() >= 0 {
		return !negativeLiteral
	}
	if in.Upper != nil && in.Upper.Cmp(zero) == 0 && in.Lower != nil && in.Lower.Sign() <= 0 {
		return negativeLiteral
	}
	// No non-zero pattern has 0 in its rounding interval because the
	// zero/smallest-subnormal tie belongs to even zero.
	return false
}
