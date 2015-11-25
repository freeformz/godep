package t

import (
	"bytes"
	"errors"
	"fmt"
	"go/build"
	"go/scanner"
	"go/token"
	"io/ioutil"
	"log"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var (
	// The Go 1.5 vendoring experiment is enabled by setting GO15VENDOREXPERIMENT=1.
	// The variable is obnoxiously long so that years from now when people find it in
	// their profiles and wonder what it does, there is some chance that a web search
	// might answer the question.
	go15VendorExperiment = os.Getenv("GO15VENDOREXPERIMENT") == "1"

	buildContext = build.Default
	goarch       = buildContext.GOARCH
	goos         = buildContext.GOOS
	cwd, _       = os.Getwd()
	cgoExclude   = map[string]bool{
		"runtime/cgo": true,
	}
	cgoSyscallExclude = map[string]bool{
		"runtime/cgo":  true,
		"runtime/race": true,
	}
	raceExclude = map[string]bool{
		"runtime/race": true,
		"runtime/cgo":  true,
		"cmd/cgo":      true,
		"syscall":      true,
		"errors":       true,
	}
	cmdCache     = map[string]*Package{}
	packageCache = map[string]*Package{}
)

// Mode flags for loadImport and download (in get.go).
const (
	// useVendor means that loadImport should do vendor expansion
	// (provided the vendoring experiment is enabled).
	// That is, useVendor means that the import path came from
	// a source file and has not been vendor-expanded yet.
	// Every import path should be loaded initially with useVendor,
	// and then the expanded version (with the /vendor/ in it) gets
	// recorded as the canonical import path. At that point, future loads
	// of that package must not pass useVendor, because
	// disallowVendor will reject direct use of paths containing /vendor/.
	useVendor = 1 << iota

	// getTestDeps is for download (part of "go get") and indicates
	// that test dependencies should be fetched too.
	getTestDeps
)

// A Package describes a single package found in a directory.
type Package struct {
	// Note: These fields are part of the go command's public API.
	// See list.go.  It is okay to add fields, but not to change or
	// remove existing ones.  Keep in sync with list.go
	Dir           string `json:",omitempty"` // directory containing package sources
	ImportPath    string `json:",omitempty"` // import path of package in dir
	ImportComment string `json:",omitempty"` // path in import comment on package statement
	Name          string `json:",omitempty"` // package name
	Doc           string `json:",omitempty"` // package documentation string
	Target        string `json:",omitempty"` // install path
	Shlib         string `json:",omitempty"` // the shared library that contains this package (only set when -linkshared)
	Goroot        bool   `json:",omitempty"` // is this package found in the Go root?
	Standard      bool   `json:",omitempty"` // is this package part of the standard Go library?
	Stale         bool   `json:",omitempty"` // would 'go install' do anything for this package?
	Root          string `json:",omitempty"` // Go root or Go path dir containing this package
	ConflictDir   string `json:",omitempty"` // Dir is hidden by this other directory

	// Source files
	GoFiles        []string `json:",omitempty"` // .go source files (excluding CgoFiles, TestGoFiles, XTestGoFiles)
	CgoFiles       []string `json:",omitempty"` // .go sources files that import "C"
	IgnoredGoFiles []string `json:",omitempty"` // .go sources ignored due to build constraints
	CFiles         []string `json:",omitempty"` // .c source files
	CXXFiles       []string `json:",omitempty"` // .cc, .cpp and .cxx source files
	MFiles         []string `json:",omitempty"` // .m source files
	HFiles         []string `json:",omitempty"` // .h, .hh, .hpp and .hxx source files
	SFiles         []string `json:",omitempty"` // .s source files
	SwigFiles      []string `json:",omitempty"` // .swig files
	SwigCXXFiles   []string `json:",omitempty"` // .swigcxx files
	SysoFiles      []string `json:",omitempty"` // .syso system object files added to package

	// Cgo directives
	CgoCFLAGS    []string `json:",omitempty"` // cgo: flags for C compiler
	CgoCPPFLAGS  []string `json:",omitempty"` // cgo: flags for C preprocessor
	CgoCXXFLAGS  []string `json:",omitempty"` // cgo: flags for C++ compiler
	CgoLDFLAGS   []string `json:",omitempty"` // cgo: flags for linker
	CgoPkgConfig []string `json:",omitempty"` // cgo: pkg-config names

	// Dependency information
	Imports []string `json:",omitempty"` // import paths used by this package
	Deps    []string `json:",omitempty"` // all (recursively) imported dependencies

	// Error information
	Incomplete bool            `json:",omitempty"` // was there an error loading this package or dependencies?
	Error      *PackageError   `json:",omitempty"` // error loading this package (not dependencies)
	DepsErrors []*PackageError `json:",omitempty"` // errors loading dependencies

	// Test information
	TestGoFiles  []string `json:",omitempty"` // _test.go files in package
	TestImports  []string `json:",omitempty"` // imports from TestGoFiles
	XTestGoFiles []string `json:",omitempty"` // _test.go files outside package
	XTestImports []string `json:",omitempty"` // imports from XTestGoFiles

	// Unexported fields are not part of the public API.
	build        *build.Package
	pkgdir       string // overrides build.PkgDir
	imports      []*Package
	deps         []*Package
	gofiles      []string // GoFiles+CgoFiles+TestGoFiles+XTestGoFiles files, absolute paths
	sfiles       []string
	allgofiles   []string             // gofiles + IgnoredGoFiles, absolute paths
	target       string               // installed file for this package (may be executable)
	fake         bool                 // synthesized package
	external     bool                 // synthesized external test package
	forceBuild   bool                 // this package must be rebuilt
	forceLibrary bool                 // this package is a library (even if named "main")
	cmdline      bool                 // defined by files listed on command line
	local        bool                 // imported via local path (./ or ../)
	localPrefix  string               // interpret ./ and ../ imports relative to this prefix
	exeName      string               // desired name for temporary executable
	coverMode    string               // preprocess Go source files with the coverage tool in this mode
	coverVars    map[string]*CoverVar // variables created by coverage analysis
	omitDWARF    bool                 // tell linker not to write DWARF information
	buildID      string               // expected build ID for generated package
	gobinSubdir  bool                 // install target would be subdir of GOBIN
}

// CoverVar holds the name of the generated coverage variables targeting the named file.
type CoverVar struct {
	File string // local file name
	Var  string // name of count struct
}

// A PackageError describes an error loading information about a package.
type PackageError struct {
	ImportStack   []string // shortest path from package named on command line to this one
	Pos           string   // position of error
	Err           string   // the error itself
	isImportCycle bool     // the error is an import cycle
	hard          bool     // whether the error is soft or hard; soft errors are ignored in some places
}

func (p *PackageError) Error() string {
	// Import cycles deserve special treatment.
	if p.isImportCycle {
		return fmt.Sprintf("%s\npackage %s\n", p.Err, strings.Join(p.ImportStack, "\n\timports "))
	}
	if p.Pos != "" {
		// Omit import stack.  The full path to the file where the error
		// is the most important thing.
		return p.Pos + ": " + p.Err
	}
	if len(p.ImportStack) == 0 {
		return p.Err
	}
	return "package " + strings.Join(p.ImportStack, "\n\timports ") + ": " + p.Err
}

func (p *Package) copyBuild(pp *build.Package) {
	p.build = pp

	if pp.PkgTargetRoot != "" && buildPkgdir != "" {
		old := pp.PkgTargetRoot
		pp.PkgRoot = buildPkgdir
		pp.PkgTargetRoot = buildPkgdir
		pp.PkgObj = filepath.Join(buildPkgdir, strings.TrimPrefix(pp.PkgObj, old))
	}

	p.Dir = pp.Dir
	p.ImportPath = pp.ImportPath
	p.ImportComment = pp.ImportComment
	p.Name = pp.Name
	p.Doc = pp.Doc
	p.Root = pp.Root
	p.ConflictDir = pp.ConflictDir
	// TODO? Target
	p.Goroot = pp.Goroot
	p.Standard = p.Goroot && p.ImportPath != "" && !strings.Contains(p.ImportPath, ".")
	p.GoFiles = pp.GoFiles
	p.CgoFiles = pp.CgoFiles
	p.IgnoredGoFiles = pp.IgnoredGoFiles
	p.CFiles = pp.CFiles
	p.CXXFiles = pp.CXXFiles
	p.MFiles = pp.MFiles
	p.HFiles = pp.HFiles
	p.SFiles = pp.SFiles
	p.SwigFiles = pp.SwigFiles
	p.SwigCXXFiles = pp.SwigCXXFiles
	p.SysoFiles = pp.SysoFiles
	p.CgoCFLAGS = pp.CgoCFLAGS
	p.CgoCPPFLAGS = pp.CgoCPPFLAGS
	p.CgoCXXFLAGS = pp.CgoCXXFLAGS
	p.CgoLDFLAGS = pp.CgoLDFLAGS
	p.CgoPkgConfig = pp.CgoPkgConfig
	p.Imports = pp.Imports
	p.TestGoFiles = pp.TestGoFiles
	p.TestImports = pp.TestImports
	p.XTestGoFiles = pp.XTestGoFiles
	p.XTestImports = pp.XTestImports
}

// usesCgo reports whether the package needs to run cgo
func (p *Package) usesCgo() bool {
	return len(p.CgoFiles) > 0
}

// usesSwig reports whether the package needs to run SWIG.
func (p *Package) usesSwig() bool {
	return len(p.SwigFiles) > 0 || len(p.SwigCXXFiles) > 0
}

// load populates p using information from bp, err, which should
// be the result of calling build.Context.Import.
func (p *Package) load(stk *importStack, bp *build.Package, err error) *Package {
	p.copyBuild(bp)

	// The localPrefix is the path we interpret ./ imports relative to.
	// Synthesized main packages sometimes override this.
	p.localPrefix = dirToImportPath(p.Dir)

	if err != nil {
		p.Incomplete = true
		err = expandScanner(err)
		p.Error = &PackageError{
			ImportStack: stk.copy(),
			Err:         err.Error(),
		}
		return p
	}

	useBindir := p.Name == "main"
	if !p.Standard {
		switch buildBuildmode {
		case "c-archive", "c-shared":
			useBindir = false
		}
	}

	if useBindir {
		// Report an error when the old code.google.com/p/go.tools paths are used.
		if goTools[p.ImportPath] == stalePath {
			newPath := strings.Replace(p.ImportPath, "code.google.com/p/go.", "golang.org/x/", 1)
			e := fmt.Sprintf("the %v command has moved; use %v instead.", p.ImportPath, newPath)
			p.Error = &PackageError{Err: e}
			return p
		}
		_, elem := filepath.Split(p.Dir)
		full := buildContext.GOOS + "_" + buildContext.GOARCH + "/" + elem
		if buildContext.GOOS != toolGOOS || buildContext.GOARCH != toolGOARCH {
			// Install cross-compiled binaries to subdirectories of bin.
			elem = full
		}
		if p.build.BinDir != gobin && goTools[p.ImportPath] == toBin {
			// Override BinDir.
			// This is from a subrepo but installs to $GOROOT/bin
			// by default anyway (like godoc).
			p.target = filepath.Join(gorootBin, elem)
		} else if p.build.BinDir != "" {
			// Install to GOBIN or bin of GOPATH entry.
			p.target = filepath.Join(p.build.BinDir, elem)
			if !p.Goroot && strings.Contains(elem, "/") && gobin != "" {
				// Do not create $GOBIN/goos_goarch/elem.
				p.target = ""
				p.gobinSubdir = true
			}
		}
		if goTools[p.ImportPath] == toTool {
			// This is for 'go tool'.
			// Override all the usual logic and force it into the tool directory.
			p.target = filepath.Join(gorootPkg, "tool", full)
		}
		if p.target != "" && buildContext.GOOS == "windows" {
			p.target += ".exe"
		}
	} else if p.local {
		// Local import turned into absolute path.
		// No permanent install target.
		p.target = ""
	} else {
		p.target = p.build.PkgObj
		if buildLinkshared {
			shlibnamefile := p.target[:len(p.target)-2] + ".shlibname"
			shlib, err := ioutil.ReadFile(shlibnamefile)
			if err == nil {
				libname := strings.TrimSpace(string(shlib))
				if buildContext.Compiler == "gccgo" {
					p.Shlib = filepath.Join(p.build.PkgTargetRoot, "shlibs", libname)
				} else {
					p.Shlib = filepath.Join(p.build.PkgTargetRoot, libname)

				}
			} else if !os.IsNotExist(err) {
				fatalf("unexpected error reading %s: %v", shlibnamefile, err)
			}
		}
	}

	importPaths := p.Imports
	// Packages that use cgo import runtime/cgo implicitly.
	// Packages that use cgo also import syscall implicitly,
	// to wrap errno.
	// Exclude certain packages to avoid circular dependencies.
	if len(p.CgoFiles) > 0 && (!p.Standard || !cgoExclude[p.ImportPath]) {
		importPaths = append(importPaths, "runtime/cgo")
	}
	if len(p.CgoFiles) > 0 && (!p.Standard || !cgoSyscallExclude[p.ImportPath]) {
		importPaths = append(importPaths, "syscall")
	}

	// Currently build mode c-shared, or -linkshared, forces
	// external linking mode, and external linking mode forces an
	// import of runtime/cgo.
	if p.Name == "main" && !p.Goroot && (buildBuildmode == "c-shared" || buildLinkshared) {
		importPaths = append(importPaths, "runtime/cgo")
	}

	// Everything depends on runtime, except runtime and unsafe.
	if !p.Standard || (p.ImportPath != "runtime" && p.ImportPath != "unsafe") {
		importPaths = append(importPaths, "runtime")
		// When race detection enabled everything depends on runtime/race.
		// Exclude certain packages to avoid circular dependencies.
		if buildRace && (!p.Standard || !raceExclude[p.ImportPath]) {
			importPaths = append(importPaths, "runtime/race")
		}
		// On ARM with GOARM=5, everything depends on math for the link.
		if p.Name == "main" && goarch == "arm" {
			importPaths = append(importPaths, "math")
		}
	}

	// Build list of full paths to all Go files in the package,
	// for use by commands like go fmt.
	p.gofiles = stringList(p.GoFiles, p.CgoFiles, p.TestGoFiles, p.XTestGoFiles)
	for i := range p.gofiles {
		p.gofiles[i] = filepath.Join(p.Dir, p.gofiles[i])
	}
	sort.Strings(p.gofiles)

	p.sfiles = stringList(p.SFiles)
	for i := range p.sfiles {
		p.sfiles[i] = filepath.Join(p.Dir, p.sfiles[i])
	}
	sort.Strings(p.sfiles)

	p.allgofiles = stringList(p.IgnoredGoFiles)
	for i := range p.allgofiles {
		p.allgofiles[i] = filepath.Join(p.Dir, p.allgofiles[i])
	}
	p.allgofiles = append(p.allgofiles, p.gofiles...)
	sort.Strings(p.allgofiles)

	// Check for case-insensitive collision of input files.
	// To avoid problems on case-insensitive files, we reject any package
	// where two different input files have equal names under a case-insensitive
	// comparison.
	f1, f2 := foldDup(stringList(
		p.GoFiles,
		p.CgoFiles,
		p.IgnoredGoFiles,
		p.CFiles,
		p.CXXFiles,
		p.MFiles,
		p.HFiles,
		p.SFiles,
		p.SysoFiles,
		p.SwigFiles,
		p.SwigCXXFiles,
		p.TestGoFiles,
		p.XTestGoFiles,
	))
	if f1 != "" {
		p.Error = &PackageError{
			ImportStack: stk.copy(),
			Err:         fmt.Sprintf("case-insensitive file name collision: %q and %q", f1, f2),
		}
		return p
	}

	// Build list of imported packages and full dependency list.
	imports := make([]*Package, 0, len(p.Imports))
	deps := make(map[string]*Package)
	for i, path := range importPaths {
		if path == "C" {
			continue
		}
		p1 := loadImport(path, p.Dir, p, stk, p.build.ImportPos[path], useVendor)
		if p1.Name == "main" {
			p.Error = &PackageError{
				ImportStack: stk.copy(),
				Err:         fmt.Sprintf("import %q is a program, not an importable package", path),
			}
			pos := p.build.ImportPos[path]
			if len(pos) > 0 {
				p.Error.Pos = pos[0].String()
			}
		}
		if p1.local {
			if !p.local && p.Error == nil {
				p.Error = &PackageError{
					ImportStack: stk.copy(),
					Err:         fmt.Sprintf("local import %q in non-local package", path),
				}
				pos := p.build.ImportPos[path]
				if len(pos) > 0 {
					p.Error.Pos = pos[0].String()
				}
			}
		}
		path = p1.ImportPath
		importPaths[i] = path
		if i < len(p.Imports) {
			p.Imports[i] = path
		}
		deps[path] = p1
		imports = append(imports, p1)
		for _, dep := range p1.deps {
			// The same import path could produce an error or not,
			// depending on what tries to import it.
			// Prefer to record entries with errors, so we can report them.
			if deps[dep.ImportPath] == nil || dep.Error != nil {
				deps[dep.ImportPath] = dep
			}
		}
		if p1.Incomplete {
			p.Incomplete = true
		}
	}
	p.imports = imports

	p.Deps = make([]string, 0, len(deps))
	for dep := range deps {
		p.Deps = append(p.Deps, dep)
	}
	sort.Strings(p.Deps)
	for _, dep := range p.Deps {
		p1 := deps[dep]
		if p1 == nil {
			panic("impossible: missing entry in package cache for " + dep + " imported by " + p.ImportPath)
		}
		p.deps = append(p.deps, p1)
		if p1.Error != nil {
			p.DepsErrors = append(p.DepsErrors, p1.Error)
		}
	}

	// unsafe is a fake package.
	if p.Standard && (p.ImportPath == "unsafe" || buildContext.Compiler == "gccgo") {
		p.target = ""
	}
	p.Target = p.target

	// The gc toolchain only permits C source files with cgo.
	if len(p.CFiles) > 0 && !p.usesCgo() && !p.usesSwig() && buildContext.Compiler == "gc" {
		p.Error = &PackageError{
			ImportStack: stk.copy(),
			Err:         fmt.Sprintf("C source files not allowed when not using cgo or SWIG: %s", strings.Join(p.CFiles, " ")),
		}
		return p
	}

	// In the absence of errors lower in the dependency tree,
	// check for case-insensitive collisions of import paths.
	if len(p.DepsErrors) == 0 {
		dep1, dep2 := foldDup(p.Deps)
		if dep1 != "" {
			p.Error = &PackageError{
				ImportStack: stk.copy(),
				Err:         fmt.Sprintf("case-insensitive import collision: %q and %q", dep1, dep2),
			}
			return p
		}
	}

	computeBuildID(p)
	return p
}

// packages returns the packages named by the
// command line arguments 'args'.  If a named package
// cannot be loaded at all (for example, if the directory does not exist),
// then packages prints an error and does not include that
// package in the results.  However, if errors occur trying
// to load dependencies of a named package, the named
// package is still returned, with p.Incomplete = true
// and details in p.DepsErrors.
func packages(args []string) []*Package {
	var pkgs []*Package
	for _, pkg := range packagesAndErrors(args) {
		if pkg.Error != nil {
			errorf("can't load package: %s", pkg.Error)
			continue
		}
		pkgs = append(pkgs, pkg)
	}
	return pkgs
}

// packagesAndErrors is like 'packages' but returns a
// *Package for every argument, even the ones that
// cannot be loaded at all.
// The packages that fail to load will have p.Error != nil.
func packagesAndErrors(args []string) []*Package {
	if len(args) > 0 && strings.HasSuffix(args[0], ".go") {
		return []*Package{goFilesPackage(args)}
	}

	args = importPaths(args)
	var pkgs []*Package
	var stk importStack
	var set = make(map[string]bool)

	for _, arg := range args {
		if !set[arg] {
			pkgs = append(pkgs, loadPackage(arg, &stk))
			set[arg] = true
		}
	}
	computeStale(pkgs...)

	return pkgs
}

// goFilesPackage creates a package for building a collection of Go files
// (typically named on the command line).  The target is named p.a for
// package p or named after the first Go file for package main.
func goFilesPackage(gofiles []string) *Package {
	// TODO: Remove this restriction.
	for _, f := range gofiles {
		if !strings.HasSuffix(f, ".go") {
			fatalf("named files must be .go files")
		}
	}

	var stk importStack
	ctxt := buildContext
	ctxt.UseAllFiles = true

	// Synthesize fake "directory" that only shows the named files,
	// to make it look like this is a standard package or
	// command directory.  So that local imports resolve
	// consistently, the files must all be in the same directory.
	var dirent []os.FileInfo
	var dir string
	for _, file := range gofiles {
		fi, err := os.Stat(file)
		if err != nil {
			fatalf("%s", err)
		}
		if fi.IsDir() {
			fatalf("%s is a directory, should be a Go file", file)
		}
		dir1, _ := filepath.Split(file)
		if dir1 == "" {
			dir1 = "./"
		}
		if dir == "" {
			dir = dir1
		} else if dir != dir1 {
			fatalf("named files must all be in one directory; have %s and %s", dir, dir1)
		}
		dirent = append(dirent, fi)
	}
	ctxt.ReadDir = func(string) ([]os.FileInfo, error) { return dirent, nil }

	var err error
	if dir == "" {
		dir = cwd
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		fatalf("%s", err)
	}

	bp, err := ctxt.ImportDir(dir, 0)
	pkg := new(Package)
	pkg.local = true
	pkg.cmdline = true
	pkg.load(&stk, bp, err)
	pkg.localPrefix = dirToImportPath(dir)
	pkg.ImportPath = "command-line-arguments"
	pkg.target = ""

	if pkg.Name == "main" {
		_, elem := filepath.Split(gofiles[0])
		exe := elem[:len(elem)-len(".go")] + exeSuffix
		if *buildO == "" {
			*buildO = exe
		}
		if gobin != "" {
			pkg.target = filepath.Join(gobin, exe)
		}
	}

	pkg.Target = pkg.target
	pkg.Stale = true

	computeStale(pkg)
	return pkg
}

// allPackagesInFS is like allPackages but is passed a pattern
// beginning ./ or ../, meaning it should scan the tree rooted
// at the given directory.  There are ... in the pattern too.
func allPackagesInFS(pattern string) []string {
	pkgs := matchPackagesInFS(pattern)
	if len(pkgs) == 0 {
		fmt.Fprintf(os.Stderr, "warning: %q matched no packages\n", pattern)
	}
	return pkgs
}

func matchPackagesInFS(pattern string) []string {
	// Find directory to begin the scan.
	// Could be smarter but this one optimization
	// is enough for now, since ... is usually at the
	// end of a path.
	i := strings.Index(pattern, "...")
	dir, _ := pathpkg.Split(pattern[:i])

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

// importPaths returns the import paths to use for the given command line.
func importPaths(args []string) []string {
	args = importPathsNoDotExpansion(args)
	var out []string
	for _, a := range args {
		if strings.Contains(a, "...") {
			if build.IsLocalImport(a) {
				out = append(out, allPackagesInFS(a)...)
			} else {
				out = append(out, allPackages(a)...)
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

// importPathsNoDotExpansion returns the import paths to use for the given
// command line, but it does no ... expansion.
func importPathsNoDotExpansion(args []string) []string {
	if len(args) == 0 {
		return []string{"."}
	}
	var out []string
	for _, a := range args {
		// Arguments are supposed to be import paths, but
		// as a courtesy to Windows developers, rewrite \ to /
		// in command-line arguments.  Handles .\... and so on.
		if filepath.Separator == '\\' {
			a = strings.Replace(a, `\`, `/`, -1)
		}

		// Put argument in canonical form, but preserve leading ./.
		if strings.HasPrefix(a, "./") {
			a = "./" + pathpkg.Clean(a)
			if a == "./." {
				a = "."
			}
		} else {
			a = pathpkg.Clean(a)
		}
		if a == "all" || a == "std" || a == "cmd" {
			out = append(out, allPackages(a)...)
			continue
		}
		out = append(out, a)
	}
	return out
}

// loadPackage is like loadImport but is used for command-line arguments,
// not for paths found in import statements.  In addition to ordinary import paths,
// loadPackage accepts pseudo-paths beginning with cmd/ to denote commands
// in the Go command directory, as well as paths to those directories.
func loadPackage(arg string, stk *importStack) *Package {
	if build.IsLocalImport(arg) {
		dir := arg
		if !filepath.IsAbs(dir) {
			if abs, err := filepath.Abs(dir); err == nil {
				// interpret relative to current directory
				dir = abs
			}
		}
		if sub, ok := hasSubdir(gorootSrc, dir); ok && strings.HasPrefix(sub, "cmd/") && !strings.Contains(sub[4:], "/") {
			arg = sub
		}
	}
	if strings.HasPrefix(arg, "cmd/") && !strings.Contains(arg[4:], "/") {
		if p := cmdCache[arg]; p != nil {
			return p
		}
		stk.push(arg)
		defer stk.pop()

		bp, err := buildContext.ImportDir(filepath.Join(gorootSrc, arg), 0)
		bp.ImportPath = arg
		bp.Goroot = true
		bp.BinDir = gorootBin
		if gobin != "" {
			bp.BinDir = gobin
		}
		bp.Root = goroot
		bp.SrcRoot = gorootSrc
		p := new(Package)
		cmdCache[arg] = p
		p.load(stk, bp, err)
		if p.Error == nil && p.Name != "main" {
			p.Error = &PackageError{
				ImportStack: stk.copy(),
				Err:         fmt.Sprintf("expected package main but found package %s in %s", p.Name, p.Dir),
			}
		}
		return p
	}

	// Wasn't a command; must be a package.
	// If it is a local import path but names a standard package,
	// we treat it as if the user specified the standard package.
	// This lets you run go test ./ioutil in package io and be
	// referring to io/ioutil rather than a hypothetical import of
	// "./ioutil".
	if build.IsLocalImport(arg) {
		bp, _ := buildContext.ImportDir(filepath.Join(cwd, arg), build.FindOnly)
		if bp.ImportPath != "" && bp.ImportPath != "." {
			arg = bp.ImportPath
		}
	}

	return loadImport(arg, cwd, nil, stk, nil, 0)
}

func makeImportValid(r rune) rune {
	// Should match Go spec, compilers, and ../../go/parser/parser.go:/isValidImport.
	const illegalChars = `!"#$%&'()*,:;<=>?[\]^{|}` + "`\uFFFD"
	if !unicode.IsGraphic(r) || unicode.IsSpace(r) || strings.ContainsRune(illegalChars, r) {
		return '_'
	}
	return r
}

// computeStale computes the Stale flag in the package dag that starts
// at the named pkgs (command-line arguments).
func computeStale(pkgs ...*Package) {
	for _, p := range packageList(pkgs) {
		p.Stale = isStale(p)
	}
}

// dirToImportPath returns the pseudo-import path we use for a package
// outside the Go path.  It begins with _/ and then contains the full path
// to the directory.  If the package lives in c:\home\gopher\my\pkg then
// the pseudo-import path is _/c_/home/gopher/my/pkg.
// Using a pseudo-import path like this makes the ./ imports no longer
// a special case, so that all the code to deal with ordinary imports works
// automatically.
func dirToImportPath(dir string) string {
	return pathpkg.Join("_", strings.Map(makeImportValid, filepath.ToSlash(dir)))
}

// shortPath returns an absolute or relative name for path, whatever is shorter.
func shortPath(path string) string {
	if rel, err := filepath.Rel(cwd, path); err == nil && len(rel) < len(path) {
		return rel
	}
	return path
}

// expandScanner expands a scanner.List error into all the errors in the list.
// The default Error method only shows the first error.
func expandScanner(err error) error {
	// Look for parser errors.
	if err, ok := err.(scanner.ErrorList); ok {
		// Prepare error with \n before each message.
		// When printed in something like context: %v
		// this will put the leading file positions each on
		// its own line.  It will also show all the errors
		// instead of just the first, as err.Error does.
		var buf bytes.Buffer
		for _, e := range err {
			e.Pos.Filename = shortPath(e.Pos.Filename)
			buf.WriteString("\n")
			buf.WriteString(e.Error())
		}
		return errors.New(buf.String())
	}
	return err
}

// allPackages returns all the packages that can be found
// under the $GOPATH directories and $GOROOT matching pattern.
// The pattern is either "all" (all packages), "std" (standard packages),
// "cmd" (standard commands), or a path including "...".
func allPackages(pattern string) []string {
	pkgs := matchPackages(pattern)
	if len(pkgs) == 0 {
		fmt.Fprintf(os.Stderr, "warning: %q matched no packages\n", pattern)
	}
	return pkgs
}

// hasPathPrefix reports whether the path s begins with the
// elements in prefix.
func hasPathPrefix(s, prefix string) bool {
	switch {
	default:
		return false
	case len(s) == len(prefix):
		return s == prefix
	case len(s) > len(prefix):
		if prefix != "" && prefix[len(prefix)-1] == '/' {
			return strings.HasPrefix(s, prefix)
		}
		return s[len(prefix)] == '/' && s[:len(prefix)] == prefix
	}
}

// treeCanMatchPattern(pattern)(name) reports whether
// name or children of name can possibly match pattern.
// Pattern is the same limited glob accepted by matchPattern.
func treeCanMatchPattern(pattern string) func(name string) bool {
	wildCard := false
	if i := strings.Index(pattern, "..."); i >= 0 {
		wildCard = true
		pattern = pattern[:i]
	}
	return func(name string) bool {
		return len(name) <= len(pattern) && hasPathPrefix(pattern, name) ||
			wildCard && strings.HasPrefix(name, pattern)
	}
}

func matchPackages(pattern string) []string {
	match := func(string) bool { return true }
	treeCanMatch := func(string) bool { return true }
	if pattern != "all" && pattern != "std" && pattern != "cmd" {
		match = matchPattern(pattern)
		treeCanMatch = treeCanMatchPattern(pattern)
	}

	have := map[string]bool{
		"builtin": true, // ignore pseudo-package that exists only for documentation
	}
	if !buildContext.CgoEnabled {
		have["runtime/cgo"] = true // ignore during walk
	}
	var pkgs []string

	for _, src := range buildContext.SrcDirs() {
		if (pattern == "std" || pattern == "cmd") && src != gorootSrc {
			continue
		}
		src = filepath.Clean(src) + string(filepath.Separator)
		root := src
		if pattern == "cmd" {
			root += "cmd" + string(filepath.Separator)
		}
		filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
			if err != nil || !fi.IsDir() || path == src {
				return nil
			}

			// Avoid .foo, _foo, and testdata directory trees.
			_, elem := filepath.Split(path)
			if strings.HasPrefix(elem, ".") || strings.HasPrefix(elem, "_") || elem == "testdata" {
				return filepath.SkipDir
			}

			name := filepath.ToSlash(path[len(src):])
			if pattern == "std" && (strings.Contains(name, ".") || name == "cmd") {
				// The name "std" is only the standard library.
				// If the name has a dot, assume it's a domain name for go get,
				// and if the name is cmd, it's the root of the command tree.
				return filepath.SkipDir
			}
			if !treeCanMatch(name) {
				return filepath.SkipDir
			}
			if have[name] {
				return nil
			}
			have[name] = true
			if !match(name) {
				return nil
			}
			_, err = buildContext.ImportDir(path, 0)
			if err != nil {
				if _, noGo := err.(*build.NoGoError); noGo {
					return nil
				}
			}
			pkgs = append(pkgs, name)
			return nil
		})
	}
	return pkgs
}

// vendoredImportPath returns the expansion of path when it appears in parent.
// If parent is x/y/z, then path might expand to x/y/z/vendor/path, x/y/vendor/path,
// x/vendor/path, vendor/path, or else stay x/y/z if none of those exist.
// vendoredImportPath returns the expanded path or, if no expansion is found, the original.
// If no expansion is found, vendoredImportPath also returns a list of vendor directories
// it searched along the way, to help prepare a useful error message should path turn
// out not to exist.
func vendoredImportPath(parent *Package, path string) (found string, searched []string) {
	if parent == nil || parent.Root == "" || !go15VendorExperiment {
		return path, nil
	}
	dir := filepath.Clean(parent.Dir)
	root := filepath.Join(parent.Root, "src")
	if !hasFilePathPrefix(dir, root) || len(dir) <= len(root) || dir[len(root)] != filepath.Separator {
		fatalf("invalid vendoredImportPath: dir=%q root=%q separator=%q", dir, root, string(filepath.Separator))
	}
	vpath := "vendor/" + path
	for i := len(dir); i >= len(root); i-- {
		if i < len(dir) && dir[i] != filepath.Separator {
			continue
		}
		// Note: checking for the vendor directory before checking
		// for the vendor/path directory helps us hit the
		// isDir cache more often. It also helps us prepare a more useful
		// list of places we looked, to report when an import is not found.
		if !isDir(filepath.Join(dir[:i], "vendor")) {
			continue
		}
		targ := filepath.Join(dir[:i], vpath)
		if isDir(targ) {
			// We started with parent's dir c:\gopath\src\foo\bar\baz\quux\xyzzy.
			// We know the import path for parent's dir.
			// We chopped off some number of path elements and
			// added vendor\path to produce c:\gopath\src\foo\bar\baz\vendor\path.
			// Now we want to know the import path for that directory.
			// Construct it by chopping the same number of path elements
			// (actually the same number of bytes) from parent's import path
			// and then append /vendor/path.
			chopped := len(dir) - i
			if chopped == len(parent.ImportPath)+1 {
				// We walked up from c:\gopath\src\foo\bar
				// and found c:\gopath\src\vendor\path.
				// We chopped \foo\bar (length 8) but the import path is "foo/bar" (length 7).
				// Use "vendor/path" without any prefix.
				return vpath, nil
			}
			return parent.ImportPath[:len(parent.ImportPath)-chopped] + "/" + vpath, nil
		}
		// Note the existence of a vendor directory in case path is not found anywhere.
		searched = append(searched, targ)
	}
	return path, searched
}

// disallowInternal checks that srcDir is allowed to import p.
// If the import is allowed, disallowInternal returns the original package p.
// If not, it returns a new package containing just an appropriate error.
func disallowInternal(srcDir string, p *Package, stk *importStack) *Package {
	// golang.org/s/go14internal:
	// An import of a path containing the element “internal”
	// is disallowed if the importing code is outside the tree
	// rooted at the parent of the “internal” directory.

	// There was an error loading the package; stop here.
	if p.Error != nil {
		return p
	}

	// The stack includes p.ImportPath.
	// If that's the only thing on the stack, we started
	// with a name given on the command line, not an
	// import. Anything listed on the command line is fine.
	if len(*stk) == 1 {
		return p
	}

	// Check for "internal" element: four cases depending on begin of string and/or end of string.
	i, ok := findInternal(p.ImportPath)
	if !ok {
		return p
	}

	// Internal is present.
	// Map import path back to directory corresponding to parent of internal.
	if i > 0 {
		i-- // rewind over slash in ".../internal"
	}
	parent := p.Dir[:i+len(p.Dir)-len(p.ImportPath)]
	if hasPathPrefix(filepath.ToSlash(srcDir), filepath.ToSlash(parent)) {
		return p
	}

	// Internal is present, and srcDir is outside parent's tree. Not allowed.
	perr := *p
	perr.Error = &PackageError{
		ImportStack: stk.copy(),
		Err:         "use of internal package not allowed",
	}
	perr.Incomplete = true
	return &perr
}

// loadImport scans the directory named by path, which must be an import path,
// but possibly a local import path (an absolute file system path or one beginning
// with ./ or ../).  A local relative path is interpreted relative to srcDir.
// It returns a *Package describing the package found in that directory.
func loadImport(path, srcDir string, parent *Package, stk *importStack, importPos []token.Position, mode int) *Package {
	stk.push(path)
	defer stk.pop()

	// Determine canonical identifier for this package.
	// For a local import the identifier is the pseudo-import path
	// we create from the full directory to the package.
	// Otherwise it is the usual import path.
	// For vendored imports, it is the expanded form.
	importPath := path
	origPath := path
	isLocal := build.IsLocalImport(path)
	var vendorSearch []string
	if isLocal {
		importPath = dirToImportPath(filepath.Join(srcDir, path))
	} else if mode&useVendor != 0 {
		path, vendorSearch = vendoredImportPath(parent, path)
		importPath = path
	}

	if p := packageCache[importPath]; p != nil {
		if perr := disallowInternal(srcDir, p, stk); perr != p {
			return perr
		}
		if mode&useVendor != 0 {
			if perr := disallowVendor(srcDir, origPath, p, stk); perr != p {
				return perr
			}
		}
		return reusePackage(p, stk)
	}

	p := new(Package)
	p.local = isLocal
	p.ImportPath = importPath
	packageCache[importPath] = p

	// Load package.
	// Import always returns bp != nil, even if an error occurs,
	// in order to return partial information.
	//
	// TODO: After Go 1, decide when to pass build.AllowBinary here.
	// See issue 3268 for mistakes to avoid.
	bp, err := buildContext.Import(path, srcDir, build.ImportComment)

	// If we got an error from go/build about package not found,
	// it contains the directories from $GOROOT and $GOPATH that
	// were searched. Add to that message the vendor directories
	// that were searched.
	if err != nil && len(vendorSearch) > 0 {
		// NOTE(rsc): The direct text manipulation here is fairly awful,
		// but it avoids defining new go/build API (an exported error type)
		// late in the Go 1.5 release cycle. If this turns out to be a more general
		// problem we could define a real error type when the decision can be
		// considered more carefully.
		text := err.Error()
		if strings.Contains(text, "cannot find package \"") && strings.Contains(text, "\" in any of:\n\t") {
			old := strings.SplitAfter(text, "\n")
			lines := []string{old[0]}
			for _, dir := range vendorSearch {
				lines = append(lines, "\t"+dir+" (vendor tree)\n")
			}
			lines = append(lines, old[1:]...)
			err = errors.New(strings.Join(lines, ""))
		}
	}
	bp.ImportPath = importPath
	if gobin != "" {
		bp.BinDir = gobin
	}
	if err == nil && !isLocal && bp.ImportComment != "" && bp.ImportComment != path &&
		(!go15VendorExperiment || (!strings.Contains(path, "/vendor/") && !strings.HasPrefix(path, "vendor/"))) {
		err = fmt.Errorf("code in directory %s expects import %q", bp.Dir, bp.ImportComment)
	}
	p.load(stk, bp, err)
	if p.Error != nil && len(importPos) > 0 {
		pos := importPos[0]
		pos.Filename = shortPath(pos.Filename)
		p.Error.Pos = pos.String()
	}

	if perr := disallowInternal(srcDir, p, stk); perr != p {
		return perr
	}
	if mode&useVendor != 0 {
		if perr := disallowVendor(srcDir, origPath, p, stk); perr != p {
			return perr
		}
	}

	return p
}

// matchPattern(pattern)(name) reports whether
// name matches pattern.  Pattern is a limited glob
// pattern in which '...' means 'any string' and there
// is no other special syntax.
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

// hasSubdir reports whether dir is a subdirectory of
// (possibly multiple levels below) root.
// If so, it sets rel to the path fragment that must be
// appended to root to reach dir.
func hasSubdir(root, dir string) (rel string, ok bool) {
	if p, err := filepath.EvalSymlinks(root); err == nil {
		root = p
	}
	if p, err := filepath.EvalSymlinks(dir); err == nil {
		dir = p
	}
	const sep = string(filepath.Separator)
	root = filepath.Clean(root)
	if !strings.HasSuffix(root, sep) {
		root += sep
	}
	dir = filepath.Clean(dir)
	if !strings.HasPrefix(dir, root) {
		return "", false
	}
	return filepath.ToSlash(dir[len(root):]), true
}

// packageList returns the list of packages in the dag rooted at roots
// as visited in a depth-first post-order traversal.
func packageList(roots []*Package) []*Package {
	seen := map[*Package]bool{}
	all := []*Package{}
	var walk func(*Package)
	walk = func(p *Package) {
		if seen[p] {
			return
		}
		seen[p] = true
		for _, p1 := range p.imports {
			walk(p1)
		}
		all = append(all, p)
	}
	for _, root := range roots {
		walk(root)
	}
	return all
}

// hasFilePathPrefix reports whether the filesystem path s begins with the
// elements in prefix.
func hasFilePathPrefix(s, prefix string) bool {
	sv := strings.ToUpper(filepath.VolumeName(s))
	pv := strings.ToUpper(filepath.VolumeName(prefix))
	s = s[len(sv):]
	prefix = prefix[len(pv):]
	switch {
	default:
		return false
	case sv != pv:
		return false
	case len(s) == len(prefix):
		return s == prefix
	case len(s) > len(prefix):
		if prefix != "" && prefix[len(prefix)-1] == filepath.Separator {
			return strings.HasPrefix(s, prefix)
		}
		return s[len(prefix)] == filepath.Separator && s[:len(prefix)] == prefix
	}
}

// findInternal looks for the final "internal" path element in the given import path.
// If there isn't one, findInternal returns ok=false.
// Otherwise, findInternal returns ok=true and the index of the "internal".
func findInternal(path string) (index int, ok bool) {
	// Four cases, depending on internal at start/end of string or not.
	// The order matters: we must return the index of the final element,
	// because the final one produces the most restrictive requirement
	// on the importer.
	switch {
	case strings.HasSuffix(path, "/internal"):
		return len(path) - len("internal"), true
	case strings.Contains(path, "/internal/"):
		return strings.LastIndex(path, "/internal/") + 1, true
	case path == "internal", strings.HasPrefix(path, "internal/"):
		return 0, true
	}
	return 0, false
}

// disallowVendor checks that srcDir is allowed to import p as path.
// If the import is allowed, disallowVendor returns the original package p.
// If not, it returns a new package containing just an appropriate error.
func disallowVendor(srcDir, path string, p *Package, stk *importStack) *Package {
	if !go15VendorExperiment {
		return p
	}

	// The stack includes p.ImportPath.
	// If that's the only thing on the stack, we started
	// with a name given on the command line, not an
	// import. Anything listed on the command line is fine.
	if len(*stk) == 1 {
		return p
	}

	if perr := disallowVendorVisibility(srcDir, p, stk); perr != p {
		return perr
	}

	// Paths like x/vendor/y must be imported as y, never as x/vendor/y.
	if i, ok := findVendor(path); ok {
		perr := *p
		perr.Error = &PackageError{
			ImportStack: stk.copy(),
			Err:         "must be imported as " + path[i+len("vendor/"):],
		}
		perr.Incomplete = true
		return &perr
	}

	return p
}

// disallowVendorVisibility checks that srcDir is allowed to import p.
// The rules are the same as for /internal/ except that a path ending in /vendor
// is not subject to the rules, only subdirectories of vendor.
// This allows people to have packages and commands named vendor,
// for maximal compatibility with existing source trees.
func disallowVendorVisibility(srcDir string, p *Package, stk *importStack) *Package {
	// The stack includes p.ImportPath.
	// If that's the only thing on the stack, we started
	// with a name given on the command line, not an
	// import. Anything listed on the command line is fine.
	if len(*stk) == 1 {
		return p
	}

	// Check for "vendor" element.
	i, ok := findVendor(p.ImportPath)
	if !ok {
		return p
	}

	// Vendor is present.
	// Map import path back to directory corresponding to parent of vendor.
	if i > 0 {
		i-- // rewind over slash in ".../vendor"
	}
	truncateTo := i + len(p.Dir) - len(p.ImportPath)
	if truncateTo < 0 || len(p.Dir) < truncateTo {
		return p
	}
	parent := p.Dir[:truncateTo]
	if hasPathPrefix(filepath.ToSlash(srcDir), filepath.ToSlash(parent)) {
		return p
	}

	// Vendor is present, and srcDir is outside parent's tree. Not allowed.
	perr := *p
	perr.Error = &PackageError{
		ImportStack: stk.copy(),
		Err:         "use of vendored package not allowed",
	}
	perr.Incomplete = true
	return &perr
}

// reusePackage reuses package p to satisfy the import at the top
// of the import stack stk.  If this use causes an import loop,
// reusePackage updates p's error information to record the loop.
func reusePackage(p *Package, stk *importStack) *Package {
	// We use p.imports==nil to detect a package that
	// is in the midst of its own loadPackage call
	// (all the recursion below happens before p.imports gets set).
	if p.imports == nil {
		if p.Error == nil {
			p.Error = &PackageError{
				ImportStack:   stk.copy(),
				Err:           "import cycle not allowed",
				isImportCycle: true,
			}
		}
		p.Incomplete = true
	}
	// Don't rewrite the import stack in the error if we have an import cycle.
	// If we do, we'll lose the path that describes the cycle.
	if p.Error != nil && !p.Error.isImportCycle && stk.shorterThan(p.Error.ImportStack) {
		p.Error.ImportStack = stk.copy()
	}
	return p
}

// findVendor looks for the last non-terminating "vendor" path element in the given import path.
// If there isn't one, findVendor returns ok=false.
// Otherwise, findInternal returns ok=true and the index of the "vendor".
//
// Note that terminating "vendor" elements don't count: "x/vendor" is its own package,
// not the vendored copy of an import "" (the empty import path).
// This will allow people to have packages or commands named vendor.
// This may help reduce breakage, or it may just be confusing. We'll see.
func findVendor(path string) (index int, ok bool) {
	// Two cases, depending on internal at start of string or not.
	// The order matters: we must return the index of the final element,
	// because the final one is where the effective import path starts.
	switch {
	case strings.Contains(path, "/vendor/"):
		return strings.LastIndex(path, "/vendor/") + 1, true
	case strings.HasPrefix(path, "vendor/"):
		return 0, true
	}
	return 0, false
}
