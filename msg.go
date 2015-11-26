package main

import (
	"fmt"

	"github.com/kr/pretty"
)

func verboseln(a ...interface{}) (int, error) {
	if verbose {
		return fmt.Println(a...)
	}
	return 0, nil
}

func verbosef(format string, a ...interface{}) (int, error) {
	if verbose {
		return fmt.Printf(format, a...)
	}
	return 0, nil
}

func pp(a ...interface{}) (int, error) {
	if verbose {
		return pretty.Print(a...)
	}
	return 0, nil
}

func ppln(a ...interface{}) (int, error) {
	if verbose {
		return pretty.Println(a...)
	}
	return 0, nil
}

func ppf(format string, a ...interface{}) (int, error) {
	if verbose {
		return pretty.Printf(format, a...)
	}
	return 0, nil
}
