package context

import (
	"regexp"
	"strings"
)

// Package represnets a package found in or used by the context
type Package struct {
	// re-evaluate
	Dir         string
	ImportPath  string
	TestGoFiles []string
	TestImports []string
	GoFiles     []string
	Imports     []string
	Goroot      bool // Package is in GOROOT (standard lib)
	Root        string
}

// matchPattern(pattern)(name) reports whether
// name matches pattern.  Pattern is a limited glob
// pattern in which '...' means 'any string' and there
// is no other special syntax.
// Taken from $GOROOT/src/cmd/go/main.go.
func matchPattern(pattern string) func(name string) bool {
	re := regexp.QuoteMeta(pattern)
	re = strings.Replace(re, `\.\.\.`, `.*`, -1)
	// Special case: foo/... matches foo too.
	if strings.HasSuffix(re, `/.*`) {
		re = re[:len(re)-len(`/.*`)] + `(/.*)?`
	}
	reg := regexp.MustCompile(`^` + re + `$`)
	return func(name string) bool {
		return reg.MatchString(name)
	}
}
