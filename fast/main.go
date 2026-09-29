// Command fast measures Internet speed the way fast.com does.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/justinswe/fast-cli/internal/engine"
	"github.com/justinswe/fast-cli/internal/output"
	"github.com/justinswe/std/errors"
)

// version is overridable at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	var (
		upload     bool
		jsonOut    bool
		singleLine bool
		timeout    time.Duration
	)
	flag.BoolVar(&upload, "u", false, "also measure upload speed and loaded latency (fast.com \"show more info\")")
	flag.BoolVar(&upload, "upload", false, "same as -u")
	flag.BoolVar(&jsonOut, "json", false, "print one JSON object on stdout at the end instead of live output")
	flag.BoolVar(&singleLine, "single-line", false, "print the final summary on a single line (omits client/server info)")
	flag.DurationVar(&timeout, "timeout", 0, "abort the whole run after this duration (0 = no cap)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: fast [-u|--upload] [--json] [--single-line] [--timeout d]\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "fast: unexpected argument %q\n", flag.Arg(0))
		flag.Usage()
		return 2
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx := sigCtx
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(sigCtx, timeout)
		defer cancel()
	}

	opts := engine.DefaultOptions()
	opts.Upload = upload
	opts.UserAgent = "fast-cli/" + version

	var r *output.Renderer
	progress := func(engine.Sample) {}
	if !jsonOut {
		r = output.NewRenderer(os.Stdout, output.IsTerminal(os.Stdout), singleLine)
		progress = r.Update
	}

	res, err := engine.Run(ctx, opts, progress)
	if err != nil {
		if r != nil {
			r.EndLine()
		}
		switch {
		case sigCtx.Err() != nil:
			return 130
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			fmt.Fprintf(os.Stderr, "fast: timed out after %s\n", timeout)
		default:
			fmt.Fprintln(os.Stderr, "fast:", err)
		}
		return 1
	}

	if jsonOut {
		if err := output.WriteJSON(os.Stdout, res, version, time.Now()); err != nil {
			fmt.Fprintln(os.Stderr, "fast:", err)
			return 1
		}
		return 0
	}
	r.Finish(res)
	return 0
}
