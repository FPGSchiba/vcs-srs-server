package utils

func indexOf[K comparable](element K, data []K) int {
	for k, v := range data {
		if element == v {
			return k
		}
	}
	return -1 //not found.
}

func Remove[K comparable](s []K, e K) []K {
	if len(s) <= 1 {
		return []K{}
	}
	i := indexOf(e, s)
	if i == -1 {
		return s
	}
	s[i] = s[len(s)-1]
	return s[:len(s)-1]
}

func FindByFunc[K any](s []K, f func(K) bool) (K, bool) {
	var zero K
	for _, v := range s {
		if f(v) {
			return v, true
		}
	}
	return zero, false
}
