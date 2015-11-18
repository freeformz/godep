package context

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"text/template"
)

func assertEqual(t *testing.T, a, b interface{}) {
	if !reflect.DeepEqual(a, b) {
		t.Errorf("%v != %v", a, b)
	}
}

func TestContextNotInGoPath(t *testing.T) {
	var err error
	gp := os.Getenv("GOPATH")
	defer os.Setenv("GOPATH", gp)
	if err := os.Setenv("GOPATH", "/foo"); err != nil {
		panic(err)
	}
	_, err = NewContext("/bar")
	if _, ok := err.(notInGOPATH); !ok {
		t.Fatal("Expected notInGOPATH error. Instead got: ", err)
	}

	// in GOPATH, but not in a GOPATH src dir
	if err := os.Setenv("GOPATH", "/bar"); err != nil {
		panic(err)
	}
	_, err = NewContext("/bar")
	if _, ok := err.(notInGOPATH); !ok {
		t.Fatal("Expected notInGOPATH error. Instead got: ", err)
	}
}

func TestContextpackageNameForPath(t *testing.T) {
	gp := os.Getenv("GOPATH")
	defer os.Setenv("GOPATH", gp)
	if err := os.Setenv("GOPATH", "/foo"); err != nil {
		panic(err)
	}
	cn, err := NewContext("/foo/src/bar")
	if err != nil {
		t.Fatal("Expected no error constructing context, but got:", err)
	}

	var cases = []struct {
		path, expected string
		err            error
	}{
		{path: "/foo/src/bar", expected: "bar", err: nil},
		{path: "/foo/src/bar/baz", expected: "bar/baz", err: nil},
		{path: "/foo/src/bar/vendor/baz", expected: "bar/vendor/baz", err: nil},
		{path: "/foo/src/bar/Godeps/_workspace/src/baz", expected: "bar/Godeps/_workspace/src/baz", err: nil},
		{path: "/bar", expected: "", err: notInGOPATH{"/bar"}},
	}

	for i, c := range cases {
		pn, err := cn.packageNameForPath(c.path)
		if err != c.err {
			t.Fatalf("Unexpected error resolving %d packageNameForPath(%s) expected (%+v) got (%+v)\n", i, c.path, c.err, err)
		}
		if pn != c.expected {
			t.Fatalf("Unexpected package name resolving %d packageNameForPath(%s) expected (%s) got (%s)\n", i, c.path, c.expected, pn)
		}
	}
}

func TestContext(t *testing.T) {
	var cases = []struct {
		comment string
		cwd     string
		// args         []string
		flagR        bool
		flagT        bool
		start        []*node
		altstart     []*node
		wantTree     []*node
		wantPackages []*node
		werr         bool
	}{
		{
			comment: "Just the local package",
			cwd:     "P",
			start: []*node{
				{"P", "",
					[]*node{
						{"main.go", pkg("P"), nil},
						{"main_test.go", pkg("P"), nil},
						{"+git", "P1", nil},
					},
				},
			},
			wantTree: []*node{
				{"P/main.go", pkg("P"), nil},
				{"P/main_test.go", pkg("P"), nil},
			},
			wantPackages: []*node{
				{"::Package", Package{
					Dir:         "P",
					ImportPath:  "P",
					Imports:     nil,
					GoFiles:     []string{"main.go"},
					TestGoFiles: []string{"main_test.go"},
				}, nil},
			},
		},
		{
			comment: "local package + stdlib import",
			cwd:     "P",
			start: []*node{
				{"P", "",
					[]*node{
						{"main.go", pkg("P", "fmt"), nil},
						{"main_test.go", pkg("P"), nil},
						{"+git", "P1", nil},
					},
				},
			},
			wantTree: []*node{
				{"P/main.go", pkg("P", "fmt"), nil},
				{"P/main_test.go", pkg("P"), nil},
			},
			wantPackages: []*node{
				{"::Package", Package{
					Dir:         "P",
					ImportPath:  "P",
					Imports:     []string{"fmt"},
					GoFiles:     []string{"main.go"},
					TestGoFiles: []string{"main_test.go"},
				}, nil},
				{"::Dep", Package{
					Dir:        filepath.Join(os.Getenv("GOROOT"), "src", "fmt"),
					ImportPath: "fmt",
				}, nil},
			},
		},
		{
			comment: "local package and sub package, importing a standard package",
			cwd:     "P",
			start: []*node{
				{"P", "",
					[]*node{
						{"main.go", pkg("P", "P/O"), nil},
						{"main_test.go", pkg("P"), nil},
						{"O/main.go", pkg("O"), nil},
						{"O/main_test.go", pkg("O"), nil},
						{"O/import_fmt.go", pkg("O", "fmt"), nil},
						{"+git", "P1", nil},
					},
				},
			},
			wantTree: []*node{
				{"P/main.go", pkg("P", "P/O"), nil},
				{"P/main_test.go", pkg("P"), nil},
				{"P/O/main.go", pkg("O"), nil},
				{"P/O/main_test.go", pkg("O"), nil},
				{"P/O/import_fmt.go", pkg("O", "fmt"), nil},
			},
			wantPackages: []*node{
				{"::Package", Package{
					Dir:         "P",
					ImportPath:  "P",
					Imports:     []string{"P/O"},
					GoFiles:     []string{"main.go"},
					TestGoFiles: []string{"main_test.go"},
				}, nil},
				{"::Package", Package{
					Dir:         filepath.Join("P", "O"),
					ImportPath:  "P/O",
					Imports:     []string{"fmt"},
					GoFiles:     []string{"import_fmt.go", "main.go"},
					TestGoFiles: []string{"main_test.go"},
				}, nil},
				{"::Dep", Package{
					Dir:        filepath.Join(os.Getenv("GOROOT"), "src", "fmt"),
					ImportPath: "fmt",
				}, nil},
			},
		},
		{
			comment: "local package with a GO15VE package",
			cwd:     "P",
			start: []*node{
				{"P", "",
					[]*node{
						{"main.go", pkg("P", "P/O"), nil},
						{"vendor/O/main.go", pkg("O"), nil},
						{"+git", "P1", nil},
					},
				},
			},
			wantTree: []*node{
				{"P/main.go", pkg("P", "P/O"), nil},
				{"P/vendor/O/main.go", pkg("O"), nil},
			},
			wantPackages: []*node{
				{"::Package", Package{
					Dir:         "P",
					ImportPath:  "P",
					Imports:     []string{"P/O"},
					GoFiles:     []string{"main.go"},
					TestGoFiles: nil,
				}, nil},
				{"::Package", Package{
					Dir:         filepath.Join("P", "vendor", "O"),
					ImportPath:  "P/vendor/O",
					Imports:     nil,
					GoFiles:     []string{"main.go"},
					TestGoFiles: nil,
				}, nil},
			},
		},
		{
			comment: "local package with a Godeps vendored package, w/o rewrite",
			cwd:     "P",
			start: []*node{
				{"P", "",
					[]*node{
						{"main.go", pkg("P", "P/O"), nil},
						{"Godeps/_workspace/src/O/main.go", pkg("O"), nil},
						{"+git", "P1", nil},
					},
				},
			},
			wantTree: []*node{
				{"P/main.go", pkg("P", "P/O"), nil},
				{"P/Godeps/_workspace/src/O/main.go", pkg("O"), nil},
			},
			wantPackages: []*node{
				{"::Package", Package{
					Dir:         "P",
					ImportPath:  "P",
					Imports:     []string{"P/O"},
					GoFiles:     []string{"main.go"},
					TestGoFiles: nil,
				}, nil},
				{"::Package", Package{
					Dir:         filepath.Join("P", "Godeps", "_workspace", "src", "O"),
					ImportPath:  "P/Godeps/_workspace/src/O",
					Imports:     nil,
					GoFiles:     []string{"main.go"},
					TestGoFiles: nil,
				}, nil},
			},
		},
		{
			comment: "local package with a Godeps vendored package, w/rewrite",
			cwd:     "P",
			start: []*node{
				{"P", "",
					[]*node{
						{"main.go", pkg("P", "P/Godeps/_workspace/src/O"), nil},
						{"Godeps/_workspace/src/O/main.go", pkg("O"), nil},
						{"+git", "P1", nil},
					},
				},
			},
			wantTree: []*node{
				{"P/main.go", pkg("P", "P/Godeps/_workspace/src/O"), nil},
				{"P/Godeps/_workspace/src/O/main.go", pkg("O"), nil},
			},
			wantPackages: []*node{
				{"::Package", Package{
					Dir:         "P",
					ImportPath:  "P",
					Imports:     []string{"P/Godeps/_workspace/src/O"},
					GoFiles:     []string{"main.go"},
					TestGoFiles: nil,
				}, nil},
				{"::Package", Package{
					Dir:         filepath.Join("P", "Godeps", "_workspace", "src", "O"),
					ImportPath:  "P/Godeps/_workspace/src/O",
					Imports:     nil,
					GoFiles:     []string{"main.go"},
					TestGoFiles: nil,
				}, nil},
			},
		},
		{
			comment: "Non local package",
			cwd:     "P",
			start: []*node{
				{"P", "",
					[]*node{
						{"main.go", pkg("P", "E"), nil},
						{"main_test.go", pkg("P"), nil},
						{"+git", "P1", nil},
					},
				},
				{"E", "",
					[]*node{
						{"main.go", pkg("E"), nil},
						{"main_test.go", pkg("E"), nil},
						{"+git", "E1", nil},
					},
				}},
			wantTree: []*node{
				{"P/main.go", pkg("P", "E"), nil},
				{"P/main_test.go", pkg("P"), nil},
				{"E/main.go", pkg("E"), nil},
				{"E/main_test.go", pkg("E"), nil},
			},
			wantPackages: []*node{
				{"::Package", Package{
					Dir:         "P",
					ImportPath:  "P",
					Imports:     []string{"E"},
					GoFiles:     []string{"main.go"},
					TestGoFiles: []string{"main_test.go"},
				}, nil},
				{"::Dep", Package{
					Dir:         "E",
					ImportPath:  "E",
					Imports:     nil,
					GoFiles:     []string{"main.go"},
					TestGoFiles: []string{"main_test.go"},
				}, nil},
			},
		},
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	var scratch = filepath.Join(wd, "godeptest")
	defer os.RemoveAll(scratch)
	for i, test := range cases {
		err = os.RemoveAll(scratch)
		if err != nil {
			t.Fatal(err)
		}
		altsrc := filepath.Join(scratch, "r2", "src")
		if test.altstart != nil {
			makeTree(t, &node{altsrc, "", test.altstart}, "")
		}
		src := filepath.Join(scratch, "r1", "src")
		makeTree(t, &node{src, "", test.start}, altsrc)

		dir := filepath.Join(src, test.cwd)
		root1 := filepath.Join(scratch, "r1")
		root2 := filepath.Join(scratch, "r2")
		err := os.Setenv("GOPATH", root1+string(os.PathListSeparator)+root2)
		if err != nil {
			panic(err)
		}

		c, err := NewContext(dir)
		if err != nil {
			t.Error(err)
		}

		checkTree(t, i, &node{src, "", test.wantTree})

		checkContext(t, c, &node{src, test.comment, test.wantPackages})
	}
}

func checkContext(t *testing.T, c Context, want *node) {
	var pWanted, dWanted int
	comment := want.body.(string)
	for _, n := range want.entries {
	NextEntry:
		switch entry := n.body.(type) {
		case Package:
			switch n.path {
			case "::Package":
				pWanted++
				entry.Dir = filepath.Join(want.path, entry.Dir)
				for _, p := range c.Packages {
					fmt.Println(entry.Dir)
					if p.Dir == entry.Dir && p.ImportPath == entry.ImportPath {
						if !reflect.DeepEqual(p.Imports, entry.Imports) {
							t.Errorf("%s: Package Imports not Equal want(%+v) got(%+v)", comment, entry.Imports, p.Imports)
						}
						if !reflect.DeepEqual(p.GoFiles, entry.GoFiles) {
							t.Errorf("%s: Package GoFiles not Equal want(%+v) got(%+v)", comment, entry.GoFiles, p.GoFiles)
						}
						if !reflect.DeepEqual(p.TestGoFiles, entry.TestGoFiles) {
							t.Errorf("%s: Package TestGoFiles not Equal want(%+v) got(%+v)", comment, entry.TestGoFiles, p.TestGoFiles)
						}
						break NextEntry
					}
				}
				t.Errorf("%s: Package not found\nPackage: %+v\nContext BP: %+v).", comment, entry, c.Packages)
			case "::Dep":
				dWanted++

			default:
				panic("unknown check type")
			}
		}
	}

	if l := len(c.Deps); l != dWanted {
		t.Errorf("%s: Wanted len(Deps) == %d, got %d instead", comment, dWanted, l)
	}
	if l := len(c.Packages); l != pWanted {
		t.Errorf("%s: Wanted len(Packages) == %d, got %d instead", comment, pWanted, l)
	}
}

/// ---- HELPERS

func walkTree(n *node, path string, f func(path string, n *node)) {
	f(path, n)
	for _, e := range n.entries {
		walkTree(e, filepath.Join(path, filepath.FromSlash(e.path)), f)
	}
}

func run(t *testing.T, dir, name string, args ...string) string {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		panic(name + " " + strings.Join(args, " ") + ": " + err.Error())
	}
	return string(out)
}

func makeTree(t *testing.T, tree *node, altpath string) (gopath string) {
	walkTree(tree, tree.path, func(path string, n *node) {
		//g, isGodeps := n.body.(*Godeps)
		body, _ := n.body.(string)
		switch {
		// case isGodeps:
		// 	for i, dep := range g.Deps {
		// 		rel := filepath.FromSlash(dep.ImportPath)
		// 		dir := filepath.Join(tree.path, rel)
		// 		if _, err := os.Stat(dir); os.IsNotExist(err) {
		// 			dir = filepath.Join(altpath, rel)
		// 		}
		// 		tag := dep.Comment
		// 		rev := strings.TrimSpace(run(t, dir, "git", "rev-parse", tag))
		// 		g.Deps[i].Rev = rev
		// 	}
		// 	os.MkdirAll(filepath.Dir(path), 0770)
		// 	f, err := os.Create(path)
		// 	if err != nil {
		// 		t.Errorf("makeTree: %v", err)
		// 		return
		// 	}
		// 	defer f.Close()
		// 	err = json.NewEncoder(f).Encode(g)
		// 	if err != nil {
		// 		t.Errorf("makeTree: %v", err)
		// 	}
		case n.path == "+git":
			dir := filepath.Dir(path)
			run(t, dir, "git", "init") // repo might already exist, but ok
			run(t, dir, "git", "add", ".")
			run(t, dir, "git", "commit", "-m", "godep")
			if body != "" {
				run(t, dir, "git", "tag", body)
			}
		case n.entries == nil && strings.HasPrefix(body, "symlink:"):
			target := strings.TrimPrefix(body, "symlink:")
			os.Symlink(target, path)
		case n.entries == nil && body == "(absent)":
			panic("is this gonna be forever")
		case n.entries == nil:
			os.MkdirAll(filepath.Dir(path), 0770)
			err := ioutil.WriteFile(path, []byte(body), 0660)
			if err != nil {
				t.Errorf("makeTree: %v", err)
			}
		default:
			os.MkdirAll(path, 0770)
		}
	})
	return gopath
}

func checkTree(t *testing.T, pos int, want *node) {
	walkTree(want, want.path, func(path string, n *node) {
		body := n.body.(string)
		switch {
		case n.path == "+git":
			panic("is this real life")
		case n.entries == nil && strings.HasPrefix(body, "symlink:"):
			panic("why is this happening to me")
		case n.entries == nil && body == "(absent)":
			body, err := ioutil.ReadFile(path)
			if !os.IsNotExist(err) {
				t.Errorf("%d checkTree: %s = %s want absent", pos, path, string(body))
				return
			}
		case n.entries == nil:
			gbody, err := ioutil.ReadFile(path)
			if err != nil {
				t.Errorf("%d checkTree: %v", pos, err)
				return
			}
			if got := string(gbody); got != body {
				t.Errorf("%d %s = got: %q want: %q", pos, path, got, body)
			}
		default:
			os.MkdirAll(path, 0770)
		}
	})
}

// node represents a file tree or a VCS repo
type node struct {
	path    string      // file name or commit type
	body    interface{} // file contents or commit tag
	entries []*node     // nil if the entry is a file
}

func pkg(name string, imports ...string) string {
	v := struct {
		Name    string
		Imports []string
	}{name, imports}
	var buf bytes.Buffer
	err := pkgtpl.Execute(&buf, v)
	if err != nil {
		panic(err)
	}
	return buf.String()
}

func decl(name string) string {
	return "var " + name + " int\n"
}

var (
	pkgtpl = template.Must(template.New("package").Parse(`package {{.Name}}

import (
{{range .Imports}}	{{printf "%q" .}}
{{end}})
`))
)
