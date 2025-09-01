package lineno

// Digits returns the number of decimal digits required to represent n.
// Handles zero and negative values.
func Digits(n int) int {
	if n == 0 {
		return 1
	}
	if n < 0 {
		n = -n
	}
	d := 0
	for n > 0 {
		n /= 10
		d++
	}
	return d
}
