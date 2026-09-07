package model

// stableRNG is SplitMix64. Keeping the algorithm in-tree makes seeded runs
// stable across Go versions and platforms.
type stableRNG struct{ state uint64 }

func newStableRNG(seed uint64) stableRNG {
	if seed == 0 {
		seed = 0x6a09e667f3bcc909
	}
	return stableRNG{state: seed}
}

func (r *stableRNG) next() uint64 {
	r.state += 0x9e3779b97f4a7c15
	z := r.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func (r *stableRNG) intn(n int) int {
	if n <= 1 {
		return 0
	}
	return int(r.next() % uint64(n))
}

func (r *stableRNG) signed(span int) int {
	if span <= 0 {
		return 0
	}
	return r.intn(2*span+1) - span
}

func (r *stableRNG) shuffle(values []int) {
	for i := len(values) - 1; i > 0; i-- {
		j := r.intn(i + 1)
		values[i], values[j] = values[j], values[i]
	}
}
