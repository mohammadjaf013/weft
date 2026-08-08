// Command weft is the Weft Agent CLI. It is intentionally thin — every real
// operation runs through the REST API handlers (or the local daemon bootstrap),
// so nothing is CLI-only.
package main

import (
	"fmt"
	"os"

	"github.com/mohammadjaf013/weft/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "weft:", err)
		os.Exit(1)
	}
}
