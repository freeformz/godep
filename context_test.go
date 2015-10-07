package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func assertEqual(t *testing.T, a, b interface{}) {
	if !reflect.DeepEqual(a, b) {
		t.Errorf("%v != %v", a, b)
	}
}

func TestContext(t *testing.T) {
	var cases = []struct {
		comment      string
		cwd          string
		args         []string
		flagR        bool
		flagT        bool
		start        []*node
		altstart     []*node
		wantTree     []*node
		wantPackages []*node
		wdep         Godeps
		werr         bool
	}{
		{
			comment: "simple case, one dependency",
			cwd:     "C",
			args:    []string{"."},
			start: []*node{
				{"C", "",
					[]*node{
						{"main.go", pkg("C", "C/D"), nil},
						{"main_test.go", pkg("C"), nil},
						{"D/main.go", pkg("D"), nil},
						{"D/import_fmt.go", pkg("D", "fmt"), nil},
						{"+git", "C1", nil},
					},
				},
				{"E", "",
					[]*node{
						{"main.go", pkg("E"), nil},
						{"+git", "E1", nil},
					},
				},
			},
			wantTree: []*node{
				{"C/main.go", pkg("C", "C/D"), nil},
				{"C/D/main.go", pkg("D"), nil},
			},
			wantPackages: []*node{
				{"::BasePackage", Package{
					Dir:         "C",
					ImportPath:  "C",
					Imports:     []string{"C/D"},
					GoFiles:     []string{"main.go"},
					TestGoFiles: []string{"main_test.go"},
				}, nil},
			},
			wdep: Godeps{
				ImportPath: "C",
				Deps: []Dependency{
					{ImportPath: "D", Comment: "D1"},
				},
			},
		},
		{
			comment: "recursive",
			cwd:     "C",
			args:    []string{"./..."},
			start: []*node{
				{"C", "",
					[]*node{
						{"main.go", pkg("C", "C/D"), nil},
						{"D/main.go", pkg("D"), nil},
						{"D/main_test.go", pkg("D"), nil},
						{"D/import_fmt.go", pkg("D", "fmt"), nil},
						{"+git", "C1", nil},
					},
				},
				{"E", "",
					[]*node{
						{"main.go", pkg("E"), nil},
						{"+git", "E1", nil},
					},
				},
			},
			wantTree: []*node{
				{"C/main.go", pkg("C", "C/D"), nil},
				{"C/D/main.go", pkg("D"), nil},
			},
			wantPackages: []*node{
				{"::BasePackage", Package{
					Dir:        "C",
					ImportPath: "C",
					Imports:    []string{"C/D"},
					GoFiles:    []string{"main.go"},
				}, nil},
				{"::BasePackage", Package{
					Dir:         filepath.Join("C", "D"),
					ImportPath:  "C/D",
					Imports:     []string{"fmt"},
					GoFiles:     []string{"import_fmt.go", "main.go"},
					TestGoFiles: []string{"main_test.go"},
				}, nil},
			},
			wdep: Godeps{
				ImportPath: "C",
				Deps: []Dependency{
					{ImportPath: "D", Comment: "D1"},
				},
			},
		},
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	var scratch = filepath.Join(wd, "godeptest")
	defer os.RemoveAll(scratch)
	for _, test := range cases {
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
		gopath := root1 + string(os.PathListSeparator) + root2
		if err != nil {
			panic(err)
		}

		c, err := NewContext(gopath, dir, test.args...)
		if err != nil {
			t.Error(err)
		}

		checkTree(t, &node{src, "", test.wantTree})

		checkContext(t, c, &node{src, test.comment, test.wantPackages})
	}
}

func checkContext(t *testing.T, c Context, want *node) {
	var bpWanted int
	comment := want.body.(string)
	for _, n := range want.entries {
	NextEntry:
		switch entry := n.body.(type) {
		case Package:
			switch n.path {
			case "::BasePackage":
				bpWanted++
				entry.Dir = filepath.Join(want.path, entry.Dir)
				for _, p := range c.BasePackages {
					if p.Dir == entry.Dir && p.ImportPath == entry.ImportPath {
						if !reflect.DeepEqual(p.Imports, entry.Imports) {
							t.Errorf("%s: BasePackage Imports not Equal want(%+v) got(%+v)", comment, entry.Imports, p.Imports)
						}
						if !reflect.DeepEqual(p.GoFiles, entry.GoFiles) {
							t.Errorf("%s: BasePackage GoFiles not Equal want(%+v) got(%+v)", comment, entry.GoFiles, p.GoFiles)
						}
						if !reflect.DeepEqual(p.TestGoFiles, entry.TestGoFiles) {
							t.Errorf("%s: BasePackage TestGoFiles not Equal want(%+v) got(%+v)", comment, entry.TestGoFiles, p.TestGoFiles)
						}
						break NextEntry
					}
				}
				t.Errorf("%s: BasePackage not found\nBasePackage: %+v\nContext BP: %+v).", comment, entry, c.BasePackages)
			default:
				panic("unknown check type")
			}
		}
	}

	if l := len(c.BasePackages); l != bpWanted {
		t.Errorf("%s: Wanted len(BasePackages) == %d, got %d instead", comment, bpWanted, l)
	}
}
