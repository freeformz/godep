package context

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// isDir returns true if the path is a directory, false otherwise
func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// assumes output like
// GOROOT="/a/path/to/somehwere"
func extractVarFromOutput(v string, out []byte) (string, error) {
	find := []byte(v + `="`)
	var i int
	i = bytes.Index(out, find)
	if i == -1 {
		return "", errors.New("Start Not Found")
	}
	out = out[i+len(find):]
	i = bytes.IndexByte(out, '"')
	if i == -1 {
		return "", errors.New("End Not Found")
	}
	return string(out[:i]), nil
}

func determineGOROOT() (string, error) {
	gr := os.Getenv("GOROOT")
	if len(gr) >= 1 {
		return gr, nil
	}
	// Ask Go
	cmd := exec.Command("go", "env")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", ErrMissingGOROOT
	}
	gr, err = extractVarFromOutput("GOROOT", out)
	if err != nil || len(gr) == 0 {
		return gr, ErrMissingGOROOT
	}
	return gr, nil
}

func packageFromPath(path string) (*Package, error) {
	if !hasGoFiles(path) {
		return nil, ErrPackageNotFound{path}
	}
	return nil, nil
}

func hasGoFiles(path string) bool {
	m, err := filepath.Glob(filepath.Join(path, "*.go"))
	return err == nil && len(m) > 0
}

func inTestData(sub string) bool {
	return strings.Contains(sub, "/testdata/") || strings.HasSuffix(sub, "/testdata") || strings.HasPrefix(sub, "testdata/") || sub == "testdata"
}

// go/build:build.go:157
func hasSubdir(root, dir string) (string, bool) {
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

func hasSubdirExpanded(root, dir string) (string, bool) {
	// Try using paths we received.
	if rel, ok := hasSubdir(root, dir); ok {
		return rel, ok
	}

	// Try expanding symlinks and comparing
	// expanded against unexpanded and
	// expanded against expanded.
	rootSym, _ := filepath.EvalSymlinks(root)
	dirSym, _ := filepath.EvalSymlinks(dir)

	if rel, ok := hasSubdir(rootSym, dir); ok {
		return rel, ok
	}
	if rel, ok := hasSubdir(root, dirSym); ok {
		return rel, ok
	}
	return hasSubdir(rootSym, dirSym)
}
