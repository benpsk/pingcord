// Command pingcord forwards GitHub activity to Discord, pinging only the
// developers each event concerns.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "pingcord:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("pingcord", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	if *showVersion {
		_, err := fmt.Fprintln(stdout, version)
		return err
	}

	return errors.New("server not implemented yet")
}
