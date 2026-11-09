// Command pingcord forwards GitHub activity to Discord, pinging only the
// developers each event concerns.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

const healthcheckTimeout = 3 * time.Second

func main() {
	os.Exit(runMain())
}

func runMain() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "pingcord:", err)
		return 1
	}
	return 0
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("pingcord", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print version and exit")
	healthcheck := fs.Bool("healthcheck", false, "check the local server's /healthz and exit (for Docker HEALTHCHECK)")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	switch {
	case *showVersion:
		_, err := fmt.Fprintln(stdout, version)
		return err
	case *healthcheck:
		return checkHealth(ctx, getenv("PORT"))
	}
	return serve(ctx, getenv, stderr)
}

// checkHealth lets distroless images (no curl) run a Docker HEALTHCHECK.
func checkHealth(ctx context.Context, port string) error {
	if port == "" {
		port = "8080"
	}
	ctx, cancel := context.WithTimeout(ctx, healthcheckTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strings.TrimSpace(port)+"/healthz", nil)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return errors.New("healthcheck: status " + resp.Status)
	}
	return nil
}
