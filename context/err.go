package context

import (
	"errors"
	"fmt"
)

var (
	// ErrMissingGOROOT : Unable to determine GOROOT
	ErrMissingGOROOT = errors.New("Unable to determine GOROOT.")
)

type unableToDeterminePackageNameError struct{ p string }

func (err unableToDeterminePackageNameError) Error() string {
	return fmt.Sprintf("Unable to determine package name for %s", err.p)
}

type notInGOPATH struct{ p string }

func (err notInGOPATH) Error() string {
	return fmt.Sprintf("Not in $GOPATH: %s", err.p)
}

// ErrackageNotFound is the context it was asked to be found
type ErrPackageNotFound struct{ p string }

func (err ErrPackageNotFound) Error() string {
	return fmt.Sprintf("Package Not Found: %s", err.p)
}
