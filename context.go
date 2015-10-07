package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type unableToDeterminePackageNameError struct {
	p string
}

func (err unableToDeterminePackageNameError) Error() string {
	return fmt.Sprintf("Unable to determine package name for %s", err.p)
}

// Context used to hold information about go packages imported by a set of
// PackageSpecs from the current working directory
type Context struct {
	// GoPath contains the individual parts of the GOPATH used by the context
	GoPath []string

	// GOROOT defaults to $GOROOT
	GoRoot string

	// ImportedPackages outside of BaseDir
	ImportedPackages []Package

	// BaseDir for the context
	BaseDir string

	// BasePackages matching the Context's PackageSpecs.
	BasePackages []Package

	// PackageSpecs used to initialize the Context
	PackageSpecs []string

	// Packages for the context
	Packages []Package
}

func hasGoFiles(path string) bool {
	m, err := filepath.Glob(filepath.Join(path, "*.go"))
	if err != nil {
		return false
	}
	return len(m) > 0
}

// Is the dir whithin our GoPath?
func (c Context) withinGoPath(dir string) bool {
	for _, p := range c.GoPath {
		if strings.HasPrefix(dir, filepath.Join(p, "src")) {
			return true
		}
	}
	return false
}

func (c Context) packageNameForPath(pkgPath string) string {
	for _, p := range c.GoPath {
		chk := filepath.Join(p, "src")
		if strings.HasPrefix(pkgPath, chk) {
			s := pkgPath[len(chk):]
			if s[0] == '/' {
				s = s[1:]
			}
			return s
		}
	}
	return ""
}

// PackageAtPath contructs a Package for the given path
func (c Context) PackageAtPath(p string) (Package, error) {
	pkg := Package{Dir: p}
	pkgName := c.packageNameForPath(p)
	if pkgName == "" {
		return pkg, unableToDeterminePackageNameError{p}
	}
	pkg.ImportPath = pkgName
	matches, err := filepath.Glob(filepath.Join(p, "*.go"))
	if err != nil {
		return pkg, err
	}
	for _, f := range matches {
		imports, err := parseGoFile(f)
		if err != nil {
			return pkg, err
		}
		fname := filepath.Base(f)
		if strings.HasSuffix(f, "_test.go") {
			pkg.TestGoFiles = append(pkg.TestGoFiles, fname)
			pkg.TestImports = append(pkg.TestImports, imports...)
		} else {
			pkg.GoFiles = append(pkg.GoFiles, fname)
			pkg.Imports = append(pkg.Imports, imports...)
		}
	}
	return pkg, nil
}

func parseGoFile(f string) ([]string, error) {
	fset := token.NewFileSet()
	pf, err := parser.ParseFile(fset, f, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var imports []string
	for _, i := range pf.Imports {
		imports = append(imports, strings.Replace(i.Path.Value, "\"", "", -1))
	}
	return imports, nil
}

func (c Context) matchPackagesInFS(pattern string) []Package {
	var dir, f string
	// Find directory to begin the scan.
	// Could be smarter but this one optimization
	// is enough for now, since ... is usually at the
	// end of a path.
	i := strings.Index(pattern, "...")
	switch i {
	case -1:
		dir, f = path.Split(pattern)
		if dir == "" {
			dir = f
		}
	default:
		dir, _ = path.Split(pattern[:i])
	}

	// pattern begins with ./ or ../.
	// path.Clean will discard the ./ but not the ../.
	// We need to preserve the ./ for pattern matching
	// and in the returned import paths.
	match := matchPattern(filepath.Join(c.BaseDir, pattern))

	dir, _ = filepath.Abs(filepath.Join(c.BaseDir, dir))

	var pkgs []Package
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

		name := filepath.ToSlash(path)
		if !match(name) {
			return nil
		}

		if !hasGoFiles(name) {
			return nil
		}

		if !c.withinGoPath(name) {
			return nil
		}
		pd, err := filepath.Abs(name)
		if err != nil {
			log.Print(err)
			return nil
		}
		pkg, err := c.PackageAtPath(pd)
		if err != nil {
			log.Print(err)
			return nil
		}
		pkgs = append(pkgs, pkg)
		return nil
	})
	return pkgs
}

// NewContext from the given goPath, for the given workingDirectory, with base
// packages sourced from pkgSpecs
func NewContext(goPath string, wd string, pkgSpecs ...string) (Context, error) {
	c := Context{
		GoPath: filepath.SplitList(goPath),
		GoRoot: os.Getenv("GOROOT"),
	}
	d, err := filepath.Abs(wd)
	fmt.Println(d)
	if err != nil {
		return c, err
	}
	c.BaseDir = d
	for _, ps := range pkgSpecs {
		c.BasePackages = append(c.BasePackages, c.matchPackagesInFS(ps)...)
	}
	return c, nil
}
