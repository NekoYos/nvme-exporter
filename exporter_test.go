package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testExporter(t *testing.T, runner commandRunner) *exporter {
	t.Helper()
	return &exporter{sysfsPath: t.TempDir(), devPath: "/dev", timeout: time.Second,
		scrapeTimeout: time.Second, run: runner, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), gate: make(chan struct{}, 1)}
}

func addController(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"model": "PM981a NVMe Samsung 512GB\n", "serial": " SN-" + name + "  \n"} {
		if err := os.WriteFile(filepath.Join(dir, key), []byte(value), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func scrape(e *exporter) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	return w
}

func TestDiscoveryMetricsAndHotplug(t *testing.T) {
	var mu sync.Mutex
	calls := make(map[string]int)
	e := testExporter(t, func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] != "smart-log" || args[2] != "--output-format=normal" {
			return nil, fmt.Errorf("unexpected command: %v", args)
		}
		mu.Lock()
		calls[args[1]]++
		mu.Unlock()
		return []byte("Warning Temperature Time : 0\nhost_write_commands : 236,864,267\n"), nil
	})
	for _, name := range []string{"nvme0", "nvme1", "nvme0n1", "nvme0n1p1", "nvme1n1p1", "notnvme"} {
		addController(t, e.sysfsPath, name)
	}
	w := scrape(e)
	if w.Code != 200 {
		t.Fatalf("HTTP %d", w.Code)
	}
	for _, want := range []string{
		"nvme_exporter_devices 2\n", "nvme_exporter_discovery_success 1\n",
		`host_write_commands{device="/dev/nvme0",model="PM981a NVMe Samsung 512GB",serial="SN-nvme0"} 236864267`,
		`warning_temperature_time{device="/dev/nvme1",model="PM981a NVMe Samsung 512GB",serial="SN-nvme1"} 0`,
	} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("missing %q in %s", want, w.Body.String())
		}
	}
	if calls["/dev/nvme0"] != 1 || calls["/dev/nvme1"] != 1 || len(calls) != 2 {
		t.Fatalf("unexpected calls: %v", calls)
	}
	// Removing the simulated controller must remove its series on the next scrape.
	if err := os.RemoveAll(filepath.Join(e.sysfsPath, "nvme1")); err != nil {
		t.Fatal(err)
	}
	addController(t, e.sysfsPath, "nvme2")
	body := scrape(e).Body.String()
	if strings.Contains(body, `device="/dev/nvme1"`) || !strings.Contains(body, `device="/dev/nvme2"`) {
		t.Fatalf("stale discovery: %s", body)
	}
}

func TestIdentityFallbackAndLabelEscaping(t *testing.T) {
	e := testExporter(t, func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "id-ctrl" {
			return []byte(`{"mn":" Model \"quoted\" ","sn":"a\\b\nc"}`), nil
		}
		return []byte("temperature : 49 C"), nil
	})
	if err := os.Mkdir(filepath.Join(e.sysfsPath, "nvme0"), 0755); err != nil {
		t.Fatal(err)
	}
	e.prefix = "nvme_"
	body := scrape(e).Body.String()
	want := `nvme_temperature{device="/dev/nvme0",model="Model \"quoted\"",serial="a\\b\nc"} 49`
	if !strings.Contains(body, want) {
		t.Fatalf("missing %s in %s", want, body)
	}
}

func TestDeviceFailureDoesNotHideHealthyDevice(t *testing.T) {
	e := testExporter(t, func(ctx context.Context, args ...string) ([]byte, error) {
		if args[1] == "/dev/nvme0" {
			return nil, fmt.Errorf("permission denied")
		}
		return []byte("temperature : 49 C"), nil
	})
	addController(t, e.sysfsPath, "nvme0")
	addController(t, e.sysfsPath, "nvme1")
	body := scrape(e).Body.String()
	for _, want := range []string{
		`nvme_exporter_device_scrape_success{device="/dev/nvme0",model="PM981a NVMe Samsung 512GB",serial="SN-nvme0"} 0`,
		`temperature{device="/dev/nvme1",model="PM981a NVMe Samsung 512GB",serial="SN-nvme1"} 49`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %s", want, body)
		}
	}
	if strings.Contains(body, `temperature{device="/dev/nvme0"`) {
		t.Fatal("failed device produced SMART samples")
	}
}

func TestDiscoveryFailureAndEmptyHost(t *testing.T) {
	e := testExporter(t, func(context.Context, ...string) ([]byte, error) { t.Error("unexpected command"); return nil, nil })
	if body := scrape(e).Body.String(); !strings.Contains(body, "nvme_exporter_devices 0\n") || !strings.Contains(body, "nvme_exporter_discovery_success 1\n") {
		t.Fatal(body)
	}
	e.sysfsPath = filepath.Join(e.sysfsPath, "missing")
	if body := scrape(e).Body.String(); !strings.Contains(body, "nvme_exporter_discovery_success 0\n") {
		t.Fatal(body)
	}
}

func TestTimeoutAndConcurrentScrapes(t *testing.T) {
	e := testExporter(t, func(ctx context.Context, args ...string) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() })
	e.timeout = 10 * time.Millisecond
	addController(t, e.sysfsPath, "nvme0")
	w := scrape(e)
	if !strings.Contains(w.Body.String(), `serial="SN-nvme0"} 0`) {
		t.Fatal(w.Body.String())
	}
	e.gate <- struct{}{}
	e.scrapeTimeout = 10 * time.Millisecond
	if w := scrape(e); w.Code != 503 {
		t.Fatalf("HTTP %d, want 503", w.Code)
	}
	<-e.gate
}

func TestReservedMetricAndMalformedSMART(t *testing.T) {
	for _, output := range []string{"nvme exporter devices: 1", "no SMART data"} {
		e := testExporter(t, func(context.Context, ...string) ([]byte, error) { return []byte(output), nil })
		addController(t, e.sysfsPath, "nvme0")
		body := scrape(e).Body.String()
		if !strings.Contains(body, `serial="SN-nvme0"} 0`) {
			t.Fatal(body)
		}
	}
}
