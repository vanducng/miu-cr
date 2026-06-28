package processor

// Total sums the items.
func Total(items []int) int {
	sum := 0
	for _, n := range items {
		sum += n
	}
	return sum
}
