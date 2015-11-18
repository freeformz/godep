package context

import "testing"

func TestExtractVarFromOutput(t *testing.T) {
	input := []byte(`
FOO="bar"
GOROOT="/foozle"
`)

	v, err := extractVarFromOutput("GOROOT", input)
	if err != nil {
		t.Fatal("Unexpected error:", err)
	}
	if v != "/foozle" {
		t.Error("Expected /foozle, but got", v)
	}

	v, err = extractVarFromOutput("FOO", input)
	if err != nil {
		t.Fatal("Unexpected error:", err)
	}
	if v != "bar" {
		t.Error("Expected bar, but got", v)
	}

}
