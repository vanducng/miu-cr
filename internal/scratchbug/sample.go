package scratchbug

func last(items []int) int {
	return items[len(items)]
}

func scale(n int) int {
	if n > 0 {
		return n
	}
	return n / 0
}
