package main

import (
	"fmt"
	"go/build"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/kr/pretty"
)

// Package represents a Go package.
type Package struct {
	Dir        string
	Root       string
	ImportPath string
	Deps       []string
	Standard   bool
	Processed  bool

	GoFiles        []string
	CgoFiles       []string
	IgnoredGoFiles []string

	TestGoFiles  []string
	TestImports  []string
	XTestGoFiles []string
	XTestImports []string

	Error struct {
		Err string
	}

	// --- New stuff for now
	Imports []string
}

// LoadPackages loads the named packages using go list -json.
// Unlike the go tool, an empty argument list is treated as an empty list; "."
// must be given explicitly if desired.
// IgnoredGoFiles will be processed and their dependencies resolved recursively
// Files with a build tag of `ignore` are skipped. Files with other build tags
// are however processed.
func LoadPackages(names ...string) (a []*Package, err error) {
	if len(names) == 0 {
		return nil, nil
	}
	pn := importPaths(names)
	fmt.Println("LoadPackages: ", pn)
	for _, i := range importPaths(names) {
		fmt.Printf("listPackage(%s)\n", i)
		p, err := listPackage(i)
		if err != nil {
			pretty.Print(err)
			return nil, err
		}
		a = append(a, p)
	}
	return a, nil
}

// resolveIgnoredGoFiles for the given pkgs, recursively
func resolveIgnoredGoFiles(pkg *Package, pc map[string]*Package) error {
	fmt.Println("resolveIgnoredGoFiles:", pkg.ImportPath)
	var allDeps []string
	allDeps = append(allDeps, pkg.ImportPath)
	allDeps = append(allDeps, pkg.Deps...)
	allDeps = append(allDeps, pkg.TestImports...)
	allDeps = append(allDeps, pkg.XTestImports...)
	allDeps = uniq(allDeps)
	spkgs, err := LoadPackages(allDeps...)
	if err != nil {
		return err
	}
	for _, sp := range spkgs {
		if pc[sp.ImportPath] != nil {
			continue
		}
		if len(sp.IgnoredGoFiles) > 0 {
			pc[sp.ImportPath] = sp
			ni, nti, err := sp.ignoredGoFilesDeps()
			fmt.Println("sp", sp.ImportPath)
			fmt.Println("ni", ni)
			fmt.Println("nti", nti)
			if err != nil {
				panic(err)
			}
			pkg.Deps = append(pkg.Deps, ni...)
			pkg.TestImports = append(pkg.TestImports, nti...)
			if len(ni) > 0 || len(nti) > 0 {
				err := resolveIgnoredGoFiles(sp, pc)
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (p *Package) ignoredGoFilesDeps() ([]string, []string, error) {
	if p.Standard {
		return nil, nil, nil
	}

	var buildMatch = "+build "
	var buildFieldSplit = func(r rune) bool {
		return unicode.IsSpace(r) || r == ','
	}
	var imports, testImports []string
	for _, fname := range p.IgnoredGoFiles {
		tgt := filepath.Join(p.Dir, fname)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, tgt, nil, parser.ParseComments)
		if err != nil {
			return nil, nil, err
		}
		if len(f.Comments) > 0 {
			for _, c := range f.Comments {
				ct := c.Text()
				if i := strings.Index(ct, buildMatch); i != -1 {
					for _, b := range strings.FieldsFunc(ct[i+len(buildMatch):], buildFieldSplit) {
						if b == "ignore" {
							continue
						}
					}
				}
			}
		}
		for _, is := range f.Imports {
			name, err := strconv.Unquote(is.Path.Value)
			if err != nil {
				return nil, nil, err // can't happen
			}
			if strings.HasSuffix(fname, "_test.go") {
				if !hasString(p.TestImports, name) {
					testImports = append(testImports, name)
				}
			} else {
				if !hasString(p.Deps, name) {
					fmt.Println("p.Deps(", p.ImportPath, ")", p.Deps)
					imports = append(imports, name)
				}
			}
		}
	}
	p.Deps = uniq(append(p.Deps, imports...))
	p.TestImports = uniq(append(p.TestImports, testImports...))
	return imports, testImports, nil
}

func hasString(search []string, s string) bool {
	sort.Strings(search)
	i := sort.SearchStrings(search, s)
	return i < len(search) && search[i] == s
}

func (p *Package) allGoFiles() []string {
	var a []string
	a = append(a, p.GoFiles...)
	a = append(a, p.CgoFiles...)
	a = append(a, p.TestGoFiles...)
	a = append(a, p.XTestGoFiles...)
	a = append(a, p.IgnoredGoFiles...)
	return a
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

// src/cmd/go/main.go:631
func matchPackagesInFS(pattern string) []string {
	// Find directory to begin the scan.
	// Could be smarter but this one optimization
	// is enough for now, since ... is usually at the
	// end of a path.
	i := strings.Index(pattern, "...")
	dir, _ := path.Split(pattern[:i])

	// pattern begins with ./ or ../.
	// path.Clean will discard the ./ but not the ../.
	// We need to preserve the ./ for pattern matching
	// and in the returned import paths.
	prefix := ""
	if strings.HasPrefix(pattern, "./") {
		prefix = "./"
	}
	match := matchPattern(pattern)

	var pkgs []string
	filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil || !fi.IsDir() {
			return nil
		}
		if path == dir {
			// filepath.Walk starts at dir and recurses. For the recursive case,
			// the path is the result of filepath.Join, which calls filepath.Clean.
			// The initial case is not Cleaned, though, so we do this explicitly.
			//
			// This converts a path like "./io/" to "io". Without this step, running
			// "cd $GOROOT/src; go list ./io/..." would incorrectly skip the io
			// package, because prepending the prefix "./" to the unclean path would
			// result in "././io", and match("././io") returns false.
			path = filepath.Clean(path)
		}

		// Avoid .foo, _foo, and testdata directory trees, but do not avoid "." or "..".
		_, elem := filepath.Split(path)
		dot := strings.HasPrefix(elem, ".") && elem != "." && elem != ".."
		if dot || strings.HasPrefix(elem, "_") || elem == "testdata" {
			return filepath.SkipDir
		}

		name := prefix + filepath.ToSlash(path)
		if !match(name) {
			return nil
		}
		if _, err = build.ImportDir(path, 0); err != nil {
			if _, noGo := err.(*build.NoGoError); !noGo {
				log.Print(err)
			}
			return nil
		}
		pkgs = append(pkgs, name)
		return nil
	})
	return pkgs
}
