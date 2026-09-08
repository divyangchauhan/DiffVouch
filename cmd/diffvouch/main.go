package main

import (
	"fmt"
	"os"

	"github.com/divyangchauhan/DiffVouch/internal/cli"
	"github.com/divyangchauhan/DiffVouch/internal/dv"
)

var version = "dev"

func main() {
	if err := cli.Execute(version); err != nil {
		fmt.Fprintf(os.Stderr, "diffvouch: %v\n", err)
		os.Exit(dv.ExitCode(err))
	}
}
