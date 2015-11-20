package context

import (
	"errors"
	"fmt"
)

var (
	// ErrMissingGOROOT you are
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

// ErrPackageNotFound is the context it was asked to be found
type ErrPackageNotFound struct{ p string }

func (err ErrPackageNotFound) Error() string {
	return fmt.Sprintf("Package Not Found: %s", err.p)
}

// ErrInvalidImportPath specified error
type ErrInvalidImportPath struct{ p string }

func (err ErrInvalidImportPath) Error() string {
	return fmt.Sprint("Invalid import path:", err.p)
}

// ErrImportRelativeUnknown directory
type ErrImportRelativeUnknown struct{ p string }

func (err ErrImportRelativeUnknown) Error() string {
	return fmt.Sprint("Import relative to unknown directory:", err.p)
}
