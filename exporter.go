package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type commandRunner func(context.Context, ...string) ([]byte, error)

type exporter struct {
	sysfsPath     string
	devPath       string
	prefix        string
	timeout       time.Duration
	scrapeTimeout time.Duration
	run           commandRunner
	logger        *slog.Logger
	gate          chan struct{}
}

type deviceResult struct {
	device  string
	model   string
	serial  string
	metrics map[string]string
	err     error
}

var controllerName = regexp.MustCompile(`^nvme[0-9]+$`)

func newRunner(binary string) commandRunner {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		output, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("nvme %v: %w: %s", args, err, strings.TrimSpace(stderr.String()))
		}
		return output, nil
	}
}

func (e *exporter) command(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	return e.run(ctx, args...)
}

func (e *exporter) discover() ([]string, error) {
	entries, err := os.ReadDir(e.sysfsPath)
	if err != nil {
		return nil, fmt.Errorf("discover NVMe controllers: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if controllerName.MatchString(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

func (e *exporter) collectDevice(ctx context.Context, name string) deviceResult {
	result := deviceResult{device: filepath.ToSlash(filepath.Join(e.devPath, name))}
	model, modelErr := os.ReadFile(filepath.Join(e.sysfsPath, name, "model"))
	serial, serialErr := os.ReadFile(filepath.Join(e.sysfsPath, name, "serial"))
	result.model = strings.TrimSpace(string(model))
	result.serial = strings.TrimSpace(string(serial))
	if modelErr != nil || serialErr != nil || result.model == "" || result.serial == "" {
		output, err := e.command(ctx, "id-ctrl", result.device, "--output-format=json")
		if err != nil {
			result.err = err
			return result
		}
		var identity struct {
			Model  string `json:"mn"`
			Serial string `json:"sn"`
		}
		if err := json.Unmarshal(output, &identity); err != nil {
			result.err = fmt.Errorf("decode id-ctrl: %w", err)
			return result
		}
		result.model = strings.TrimSpace(identity.Model)
		result.serial = strings.TrimSpace(identity.Serial)
		if result.model == "" || result.serial == "" {
			result.err = fmt.Errorf("id-ctrl returned empty model or serial number")
			return result
		}
	}
	output, err := e.command(ctx, "smart-log", result.device, "--output-format=json")
	if err != nil {
		result.err = err
		return result
	}
	result.metrics, result.err = parseSMART(string(output))
	for name := range result.metrics {
		if strings.HasPrefix(e.prefix+name, "nvme_exporter_") {
			result.err = fmt.Errorf("SMART metric uses reserved nvme_exporter_ prefix: %s", name)
			break
		}
	}
	return result
}

func escapeLabel(value string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"").Replace(value)
}

func (r deviceResult) labels() string {
	return fmt.Sprintf(`{device="%s",model="%s",serial="%s"}`, escapeLabel(r.device), escapeLabel(r.model), escapeLabel(r.serial))
}

func (e *exporter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), e.scrapeTimeout)
	defer cancel()
	// Concurrent Prometheus scrapes must not pile up commands against the drives.
	select {
	case e.gate <- struct{}{}:
		defer func() { <-e.gate }()
	case <-ctx.Done():
		http.Error(w, "scrape deadline exceeded", http.StatusServiceUnavailable)
		return
	}
	start := time.Now()
	names, err := e.discover()
	discoverySuccess := 1
	if err != nil {
		discoverySuccess = 0
		e.logger.Error("NVMe discovery failed", "error", err)
	}
	results := make([]deviceResult, len(names))
	var wg sync.WaitGroup
	// At most four commands run simultaneously, even on hosts with many drives.
	jobs := make(chan int)
	for worker := 0; worker < min(4, len(names)); worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i] = e.collectDevice(ctx, names[i])
			}
		}()
	}
	for i := range names {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	var body bytes.Buffer
	fmt.Fprintln(&body, "# HELP nvme_exporter_discovery_success Whether controller discovery succeeded.")
	fmt.Fprintln(&body, "# TYPE nvme_exporter_discovery_success gauge")
	fmt.Fprintf(&body, "nvme_exporter_discovery_success %d\n", discoverySuccess)
	fmt.Fprintln(&body, "# HELP nvme_exporter_devices Number of discovered NVMe controllers.")
	fmt.Fprintln(&body, "# TYPE nvme_exporter_devices gauge")
	fmt.Fprintf(&body, "nvme_exporter_devices %d\n", len(names))
	fmt.Fprintln(&body, "# HELP nvme_exporter_device_scrape_success Whether identity and SMART collection succeeded.")
	fmt.Fprintln(&body, "# TYPE nvme_exporter_device_scrape_success gauge")
	families := make(map[string][]string)
	for _, result := range results {
		success := 1
		if result.err != nil {
			success = 0
			e.logger.Error("NVMe collection failed", "device", result.device, "error", result.err)
		}
		fmt.Fprintf(&body, "nvme_exporter_device_scrape_success%s %d\n", result.labels(), success)
		if success == 0 {
			continue
		}
		for name, value := range result.metrics {
			name = e.prefix + name
			families[name] = append(families[name], name+result.labels()+" "+value+"\n")
		}
	}
	keys := make([]string, 0, len(families))
	for name := range families {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		// The dynamic field name alone cannot reliably distinguish gauges and counters.
		fmt.Fprintf(&body, "# HELP %s Numeric field from nvme smart-log.\n# TYPE %s untyped\n", name, name)
		for _, sample := range families[name] {
			body.WriteString(sample)
		}
	}
	fmt.Fprintln(&body, "# HELP nvme_exporter_scrape_duration_seconds Time spent discovering and collecting NVMe devices.")
	fmt.Fprintln(&body, "# TYPE nvme_exporter_scrape_duration_seconds gauge")
	fmt.Fprintf(&body, "nvme_exporter_scrape_duration_seconds %g\n", time.Since(start).Seconds())
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body.Bytes())
}
