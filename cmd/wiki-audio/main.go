// Command wiki-audio is the CLI for synthesizing PG essays into a
// personal podcast feed. See PLAN_FOR_AUDIO_LIBRARY.md §3.
package main

import (
	"fmt"
	"os"

	"github.com/Jacob2017/wiki-audio/internal/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
