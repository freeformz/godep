package context

import (
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Context used to hold information about go packages imported by a set of
// PackageSpecs from the current working directory
type Context struct {
	// GoPath contains the individual parts of the GOPATH used by the context
	GOPATH []string

	// GOROOT defaults to $GOROOT
	GOROOT string

	// Root of the context
	Root string

	// The last error
	Err error

	// Packages in the context
	Packages []*Package

	// Deps (Dependencies) that this context depends on that are outside of the context
	Deps []*Package

	// Cache of packages map[Package.Dir]*Package
	cache map[string]*Package
}

func hasGoFiles(path string) bool {
	m, err := filepath.Glob(filepath.Join(path, "*.go"))
	if err != nil {
		return false
	}
	return len(m) > 0
}

// dirInPaths determines if dir is in any of the provided paths
// dir and path elements are rendered to their Abs values before comparison
func dirInPaths(dir string, paths ...string) bool {
	var found bool
	var err error
	dir, err = filepath.Abs(dir)
	if err != nil {
		return false
	}
	for _, p := range paths {
		p, err = filepath.Abs(p)
		if err != nil {
			return false
		}
		if strings.HasPrefix(dir, p) {
			found = true
			break
		}
	}
	return found
}

func (c Context) packageNameForPath(pkgPath string) (string, error) {
	for _, p := range c.GOPATH {
		chk := filepath.Join(p, "src")
		if strings.HasPrefix(pkgPath, chk) {
			s := pkgPath[len(chk):]
			if s[0] == '/' {
				s = s[1:]
			}
			return s, nil
		}
	}
	return "", notInGOPATH{pkgPath}
}

// PackageAtPath contructs a Package for the given path
func (c Context) PackageAtPath(p string) (Package, error) {
	pkg := Package{Dir: p}
	p, err := filepath.Abs(p)
	if err != nil {
		return pkg, err
	}
	pkg.Dir = p
	pkgName, err := c.packageNameForPath(p)
	if err != nil {
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

func parseGoFile(fp string) ([]string, error) {
	fset := token.NewFileSet()
	pf, err := parser.ParseFile(fset, fp, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var imports []string
	for _, i := range pf.Imports {
		imports = append(imports, strings.Replace(i.Path.Value, `"`, "", -1))
	}
	return imports, nil
}

func (c Context) packagesInContext() []*Package {
	var pkgs []*Package
	filepath.Walk(c.Root, func(path string, f os.FileInfo, err error) error {
		// Skip all errors and directories
		if err != nil || !f.IsDir() {
			return nil
		}

		if path == c.Root {
			// filepath.Walk starts at dir and recurses. For the recursive case,
			// the path is the result of filepath.Join, which calls filepath.Clean.
			// The initial case is not Cleaned, though, so we do this explicitly.
			path = filepath.Clean(path)
		}

		// Avoid .foo, and testdata directory trees, but do not avoid "." or "..".
		p, elem := filepath.Split(path)
		p = filepath.Clean(p) //strip a trailing /
		dot := strings.HasPrefix(elem, ".") && elem != "." && elem != ".."
		if dot || elem == "testdata" {
			return filepath.SkipDir
		}
		godepWorkspace := strings.HasSuffix(p, "Godeps") && elem == "_workspace"
		if strings.HasPrefix(elem, "_") && !godepWorkspace {
			return filepath.SkipDir
		}

		if !hasGoFiles(path) {
			return nil
		}

		pkg, err := c.PackageAtPath(path)
		if err != nil {
			log.Print(err)
			return nil
		}
		pkgs = append(pkgs, &pkg)
		return nil
	})
	return pkgs
}

func (c Context) dirInGOPATH(d string) bool {
	var found bool
	for _, gp := range c.GOPATH {
		found = dirInPaths(d, filepath.Join(gp, "src"))
		if found {
			break
		}
	}
	return found
}

// Finds the package corresponding to the package name if it exists in the
// context's GOROOT.
// Returns ErrPackageNotFound otherwise.
func (c Context) findImportInGOROOT(name string) (*Package, error) {
	return findImportInDir(filepath.Join(c.GOROOT, "src"), name)
}

func (c Context) findImportInVendor(name string) (*Package, error) {
	return nil, os.ErrNotExist
}

func (c Context) findImportInGodepsVendor(name string) (*Package, error) {
	return nil, os.ErrNotExist
}

func findImportInDir(dir, name string) (*Package, error) {
	l := filepath.FromSlash(filepath.Join(dir, name))
	fi, err := os.Stat(l)
	if err != nil {
		return nil, ErrPackageNotFound{name}
	}
	if !fi.IsDir() {
		return nil, ErrPackageNotFound{name}
	}
	return &Package{Dir: l, ImportPath: name}, nil
}

// findImports for the name, requested from inside of dir
func (c Context) findImport(dir, name string) (*Package, error) {
	// First check to see if it's in GOROOT
	p, err := c.findImportInGOROOT(name)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return p, nil
}

func (c *Context) resolveDeps() error {
	for _, p := range c.Packages {
		for _, i := range p.Imports {

			if i == "C" {
				continue
			}
			p, err := c.findImport(p.Dir, i)
			if err != nil {
				return err
			}
			c.Deps = append(c.Deps, p)
		}
	}
	return nil
}

// NewContext for the given workingDirectory
func NewContext(wd string) (Context, error) {
	c := Context{cache: make(map[string]*Package)}
	for _, gp := range filepath.SplitList(os.Getenv("GOPATH")) {
		gp, err := filepath.Abs(gp)
		if err != nil {
			return c, err
		}
		c.GOPATH = append(c.GOPATH, gp)
	}
	gr, err := determineGOROOT()
	if err != nil {
		return c, err
	}
	c.GOROOT = gr
	d, err := filepath.Abs(wd)
	if err != nil {
		return c, err
	}
	if ok := c.dirInGOPATH(d); !ok {
		return c, notInGOPATH{d}
	}
	c.Root = d
	c.Packages = append(c.Packages, c.packagesInContext()...)
	for _, p := range c.Packages {
		c.cache[p.Dir] = p
	}

	return c, c.resolveDeps()
}
