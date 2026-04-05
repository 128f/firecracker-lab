package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/128f/fctl/cmd"
)

func main() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := cmd.Execute(filepath.Dir(exe)); err != nil {
		os.Exit(1)
	}
}
