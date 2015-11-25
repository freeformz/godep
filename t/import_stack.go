package t

// An importStack is a stack of import paths.
type importStack []string

func (s *importStack) push(p string) {
	*s = append(*s, p)
}

func (s *importStack) pop() {
	*s = (*s)[0 : len(*s)-1]
}

func (s *importStack) copy() []string {
	return append([]string{}, *s...)
}

// shorterThan reports whether sp is shorter than t.
// We use this to record the shortest import sequence
// that leads to a particular package.
func (s importStack) shorterThan(t []string) bool {
	if len(s) != len(t) {
		return len(s) < len(t)
	}
	// If they are the same length, settle ties using string ordering.
	for i := range s {
		if s[i] != t[i] {
			return s[i] < t[i]
		}
	}
	return false // they are equal
}
