package utils

func Remove[K comparable](s []K, e K) []K {
	result := make([]K, 0, len(s))
	for _, v := range s {
		if v != e {
			result = append(result, v)
		}
	}
	return result
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
