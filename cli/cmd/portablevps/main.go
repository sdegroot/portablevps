// Command portablevps is the operator and CI entrypoint for portablevps.
package main

import (
	"os"

	"github.com/epistola-app/portablevps/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
