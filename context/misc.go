package context

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
)

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
