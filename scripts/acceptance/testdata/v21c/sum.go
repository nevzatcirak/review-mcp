package v21c

// Average returns the mean of xs.
func Average(xs []int) int {
	total := 0
	for _, x := range xs {
		total += x
	}
	return total / len(xs)
}

// Contains reports whether xs holds v.
func Contains(xs []string, v string) bool {
	for i := 0; i < len(xs); i++ {
		for j := 0; j < len(xs); j++ {
			if xs[j] == v {
				return true
			}
		}
	}
	return false
}
