package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("Exporter stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	listen := flag.String("listen-address", ":9998", "HTTP listen address")
	binary := flag.String("nvme-path", "nvme", "Path to nvme-cli executable")
	sysfs := flag.String("sysfs-path", "/sys/class/nvme", "NVMe controller class directory")
	dev := flag.String("dev-path", "/dev", "Device node directory")
	prefix := flag.String("metric-prefix", "", "Optional prefix for SMART metric names, e.g. nvme_")
	timeout := flag.Duration("command-timeout", 5*time.Second, "Timeout for each nvme invocation")
	scrapeTimeout := flag.Duration("scrape-timeout", 20*time.Second, "Total collection timeout, including waiting for another scrape")
	flag.Parse()
	if *timeout <= 0 || *scrapeTimeout <= 0 {
		return fmt.Errorf("timeouts must be positive")
	}
	if !regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_]*)?$`).MatchString(*prefix) ||
		strings.HasPrefix(*prefix, "nvme_exporter_") {
		return fmt.Errorf("metric-prefix must be a valid Prometheus name prefix outside nvme_exporter_")
	}
	e := &exporter{
		sysfsPath: *sysfs, devPath: *dev, prefix: *prefix,
		timeout: *timeout, scrapeTimeout: *scrapeTimeout,
		run: newRunner(*binary), logger: logger, gate: make(chan struct{}, 1),
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", e)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprintln(w, "ok") })
	server := &http.Server{
		Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout: *scrapeTimeout + 5*time.Second, IdleTimeout: 60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				_ = server.Close()
			}
		case <-done:
		}
	}()
	logger.Info("Starting NVMe exporter", "address", *listen)
	err := server.ListenAndServe()
	close(done)
	<-shutdownDone
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
