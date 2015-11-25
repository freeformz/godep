package t

import (
	"bytes"
	"crypto/sha1"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

var (
	exitStatus = 0
	exitMu     sync.Mutex

	buildA          bool   // -a flag
	buildPkgdir     string // -pkgdir flag
	buildBuildmode  string // -buildmode flag
	buildLinkshared bool   // -linkshared flag
	buildRace       bool   // -race flag
	buildO          = flag.String("o", "", "output file")

	toolGOOS   = runtime.GOOS
	toolGOARCH = runtime.GOARCH

	goroot    = filepath.Clean(runtime.GOROOT())
	gobin     = os.Getenv("GOBIN")
	gorootBin = filepath.Join(goroot, "bin")
	gorootPkg = filepath.Join(goroot, "pkg")
	gorootSrc = filepath.Join(goroot, "src")

	exeSuffix   string
	isGoRelease = strings.HasPrefix(runtime.Version(), "go1")
)

type targetDir int

const (
	toRoot    targetDir = iota // to bin dir inside package root (default)
	toTool                     // GOROOT/pkg/tool
	toBin                      // GOROOT/bin
	stalePath                  // the old import path; fail to build
)

// goTools is a map of Go program import path to install target directory.
var goTools = map[string]targetDir{
	"cmd/addr2line":                        toTool,
	"cmd/api":                              toTool,
	"cmd/asm":                              toTool,
	"cmd/compile":                          toTool,
	"cmd/cgo":                              toTool,
	"cmd/cover":                            toTool,
	"cmd/dist":                             toTool,
	"cmd/doc":                              toTool,
	"cmd/fix":                              toTool,
	"cmd/link":                             toTool,
	"cmd/newlink":                          toTool,
	"cmd/nm":                               toTool,
	"cmd/objdump":                          toTool,
	"cmd/pack":                             toTool,
	"cmd/pprof":                            toTool,
	"cmd/trace":                            toTool,
	"cmd/vet":                              toTool,
	"cmd/yacc":                             toTool,
	"golang.org/x/tools/cmd/godoc":         toBin,
	"code.google.com/p/go.tools/cmd/cover": stalePath,
	"code.google.com/p/go.tools/cmd/godoc": stalePath,
	"code.google.com/p/go.tools/cmd/vet":   stalePath,
}

func errorf(format string, args ...interface{}) {
	log.Printf(format, args...)
	setExitStatus(1)
}

func setExitStatus(n int) {
	exitMu.Lock()
	if exitStatus < n {
		exitStatus = n
	}
	exitMu.Unlock()
}

var atexitFuncs []func()

func exit() {
	for _, f := range atexitFuncs {
		f()
	}
	os.Exit(exitStatus)
}

func fatalf(format string, args ...interface{}) {
	errorf(format, args...)
	exit()
}

// stringList's arguments should be a sequence of string or []string values.
// stringList flattens them into a single []string.
func stringList(args ...interface{}) []string {
	var x []string
	for _, arg := range args {
		switch arg := arg.(type) {
		case []string:
			x = append(x, arg...)
		case string:
			x = append(x, arg)
		default:
			panic("stringList: invalid argument of type " + fmt.Sprintf("%T", arg))
		}
	}
	return x
}

// foldDup reports a pair of strings from the list that are
// equal according to strings.EqualFold.
// It returns "", "" if there are no such strings.
func foldDup(list []string) (string, string) {
	clash := map[string]string{}
	for _, s := range list {
		fold := toFold(s)
		if t := clash[fold]; t != "" {
			if s > t {
				s, t = t, s
			}
			return s, t
		}
		clash[fold] = s
	}
	return "", ""
}

// toFold returns a string with the property that
//	strings.EqualFold(s, t) iff toFold(s) == toFold(t)
// This lets us test a large set of strings for fold-equivalent
// duplicates without making a quadratic number of calls
// to EqualFold. Note that strings.ToUpper and strings.ToLower
// have the desired property in some corner cases.
func toFold(s string) string {
	// Fast path: all ASCII, no upper case.
	// Most paths look like this already.
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= utf8.RuneSelf || 'A' <= c && c <= 'Z' {
			goto Slow
		}
	}
	return s

Slow:
	var buf bytes.Buffer
	for _, r := range s {
		// SimpleFold(x) cycles to the next equivalent rune > x
		// or wraps around to smaller values. Iterate until it wraps,
		// and we've found the minimum value.
		for {
			r0 := r
			r = unicode.SimpleFold(r0)
			if r <= r0 {
				break
			}
		}
		// Exception to allow fast path above: A-Z => a-z
		if 'A' <= r && r <= 'Z' {
			r += 'a' - 'A'
		}
		buf.WriteRune(r)
	}
	return buf.String()
}

// computeBuildID computes the build ID for p, leaving it in p.buildID.
// Build ID is a hash of the information we want to detect changes in.
// See the long comment in isStale for details.
func computeBuildID(p *Package) {
	h := sha1.New()

	// Include the list of files compiled as part of the package.
	// This lets us detect removed files. See issue 3895.
	inputFiles := stringList(
		p.GoFiles,
		p.CgoFiles,
		p.CFiles,
		p.CXXFiles,
		p.MFiles,
		p.HFiles,
		p.SFiles,
		p.SysoFiles,
		p.SwigFiles,
		p.SwigCXXFiles,
	)
	for _, file := range inputFiles {
		fmt.Fprintf(h, "file %s\n", file)
	}

	// Include the content of runtime/zversion.go in the hash
	// for package runtime. This will give package runtime a
	// different build ID in each Go release.
	if p.Standard && p.ImportPath == "runtime" {
		data, _ := ioutil.ReadFile(filepath.Join(p.Dir, "zversion.go"))
		fmt.Fprintf(h, "zversion %q\n", string(data))
	}

	// Include the build IDs of any dependencies in the hash.
	// This, combined with the runtime/zversion content,
	// will cause packages to have different build IDs when
	// compiled with different Go releases.
	// This helps the go command know to recompile when
	// people use the same GOPATH but switch between
	// different Go releases. See issue 10702.
	// This is also a better fix for issue 8290.
	for _, p1 := range p.deps {
		fmt.Fprintf(h, "dep %s %s\n", p1.ImportPath, p1.buildID)
	}

	p.buildID = fmt.Sprintf("%x", h.Sum(nil))
}

// isStale stub
func isStale(p *Package) bool {
	return false
}

var isDirCache = map[string]bool{}

func isDir(path string) bool {
	result, ok := isDirCache[path]
	if ok {
		return result
	}

	fi, err := os.Stat(path)
	result = err == nil && fi.IsDir()
	isDirCache[path] = result
	return result
}
