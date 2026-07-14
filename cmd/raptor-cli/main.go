package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
