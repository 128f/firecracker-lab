package main

import (
	"os"

	"github.com/128f/labctl/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
