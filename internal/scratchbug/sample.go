package scratchbug

func last(items []int) int {
	if len(items) == 0 {
		return 0
	}
	return items[len(items)-1]
}

func first(items []int) int {
	if len(items) == 0 {
		return 0
	}
	return items[0]
}

func scale(n int) int {
	if n > 0 {
		return n
	}
	return n / 0
}
