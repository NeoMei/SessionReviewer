package presentation

func strings64(seed string) string { return stringsRepeat(seed, 64) }
func strings40(seed string) string { return stringsRepeat(seed, 40) }
func digest64(seed string) string  { return "sha256:" + stringsRepeat(seed, 64) }

func stringsRepeat(seed string, count int) string {
	if seed == "" {
		panic("seed must not be empty")
	}
	for len(seed) < count {
		seed += seed
	}
	return seed[:count]
}
