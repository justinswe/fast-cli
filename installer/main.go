// Command installer serves the versioned fast installation script.
package main

import (
	"context"
	_ "embed"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/justinswe/std/app"
	"github.com/justinswe/std/errors"
	"github.com/spf13/cobra"
)

//go:embed install.sh
var installerScript string

var version = "dev"

// handler serves the installer and its health check.
func handler() http.Handler {
	script := strings.ReplaceAll(installerScript, "@VERSION@", version)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, script)
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_, _ = io.WriteString(w, "ok\n")
	})
	return mux
}

func main() {
	var port int
	cmd := &cobra.Command{
		Use:           "installer",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serve(cmd.Context(), port)
		},
	}
	cmd.Flags().IntVar(&port, "port", 8080, "HTTP listen port")
	if err := app.RunCobraCommand(context.Background(), cmd); err != nil {
		log.Fatal(err)
	}
}

// serve runs HTTP until its context is cancelled.
func serve(ctx context.Context, port int) error {
	server := &http.Server{Addr: ":" + strconv.Itoa(port), Handler: handler()}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return errors.Wrap(err, "serve installer")
	}
	return nil
}
