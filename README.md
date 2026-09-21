# NVMe Prometheus Exporter

A Go exporter for Linux and Synology DSM. The Alpine-based image includes
`nvme-cli`, so the utility does not need to be installed on the NAS. The Go
application has no third-party dependencies.

On every `/metrics` request, the exporter enumerates controllers matching
`/sys/class/nvme/nvmeN` and runs
`nvme smart-log /dev/nvmeN --output-format=normal`. Namespaces and partitions do
not create duplicates because SMART data is collected at the controller level.
The model and serial number are read from the `model` and `serial` sysfs files.
When either value is missing or empty, the exporter falls back to
`nvme id-ctrl /dev/nvmeN --output-format=json`.

The exporter does not parse the `nvme list` table, so spaces in model names and
duplicate partition rows do not affect device discovery.

## Running on Synology

Copy `compose.yaml` to a NAS with Container Manager or Docker installed, open
its directory over SSH, and run:

```sh
sudo docker compose pull
sudo docker compose up -d
curl http://localhost:9998/metrics
```

Older DSM releases may use `docker-compose` instead. You can also create a
project in Container Manager and import `compose.yaml`. Compose pulls
`nekoyos/nvme-exporter:latest` from Docker Hub; no local build is required.

To run the published image without Compose:

```sh
sudo docker run -d --name nvme-exporter \
  --restart unless-stopped --privileged --read-only \
  -p 9998:9998 \
  -v /dev:/dev -v /sys:/sys:ro \
  nekoyos/nvme-exporter:latest
```

The container runs as root. The `privileged` setting and `/dev` mount provide
access to NVMe admin ioctls and newly added devices without recreating the
container. The `/sys` mount is used for discovery and device identification.
This configuration gives the container broad access to NAS devices, so expose
the HTTP port only to a trusted monitoring network. The exporter itself runs
only the read-only `smart-log` and `id-ctrl` commands.

For a fixed set of drives, replace `privileged` and the full `/dev` mount with
individual devices such as `--device=/dev/nvme0 --device=/dev/nvme1`. Some DSM
kernels may also require `--cap-add=SYS_ADMIN`. New controllers must then be
added to the container configuration. NVMe ioctl compatibility depends on the
DSM kernel and must be verified on the NAS.

To update an existing Compose installation to the newest published image:

```sh
sudo docker compose pull
sudo docker compose up -d
```

The current published image supports `linux/amd64`.

## Metrics

SMART metric names are generated dynamically. Names are converted to lowercase,
and spaces or other characters that are invalid in Prometheus metric names are
replaced with `_`. No prefix is used by default; an optional prefix can be set
with `--metric-prefix=nvme_`.

```prometheus
temperature{device="/dev/nvme0",model="PM981a NVMe Samsung 512GB",serial="S4GTNF0MA61796"} 49
host_write_commands{device="/dev/nvme0",model="PM981a NVMe Samsung 512GB",serial="S4GTNF0MA61796"} 236864267
warning_temperature_time{device="/dev/nvme0",model="PM981a NVMe Samsung 512GB",serial="S4GTNF0MA61796"} 0
temperature_sensor_2{device="/dev/nvme0",model="PM981a NVMe Samsung 512GB",serial="S4GTNF0MA61796"} 71
```

The parser uses the first number in each value. Thousands separators, `%`, and
unit suffixes are removed. For example, `236,864,267` becomes `236864267`,
`49 C` becomes `49`, `49 °C (322 Kelvin)` becomes `49`, and
`12,162,788 [6.22 TB]` becomes `12162788`.

Signs and decimal points are preserved. Hexadecimal values such as `0x04` are
converted to decimal. Fields without a numeric value are skipped.

Units remain exactly as reported by `nvme-cli`: percentages are not divided by
100, and `data_units_*` values are not converted to bytes. Integer values are
preserved without precision loss while the response is generated, although
Prometheus itself stores numbers as float64.

Dynamic SMART metrics use the Prometheus `untyped` type because the correct type
of an arbitrary new field cannot be inferred reliably from its name. Duplicate
names after normalization cause collection for that device to fail, preventing
invalid duplicate time series from being exposed.

Exporter metrics:

| Metric | Meaning |
| --- | --- |
| `nvme_exporter_discovery_success` | `1` when the controller directory was read successfully; otherwise `0` |
| `nvme_exporter_devices` | Number of discovered NVMe controllers |
| `nvme_exporter_device_scrape_success{device,model,serial}` | `1` when identity and SMART collection succeeded; otherwise `0` |
| `nvme_exporter_scrape_duration_seconds` | Time spent collecting a scrape |

The `nvme_exporter_` prefix is reserved for exporter metrics. A failure on one
drive does not hide data from healthy drives. When SMART collection fails, stale
SMART values for that drive are not returned.

An existing but empty controller directory is a successful discovery of zero
drives. A missing or unreadable directory is a discovery failure. The `/metrics`
endpoint remains available when an individual drive fails, so alerts should
check exporter health metrics in addition to Prometheus `up`. The `/healthz`
endpoint checks only the HTTP server, not drive health.

## Prometheus

Add the following scrape configuration and replace `nas.example.local` with the
NAS address:

```yaml
scrape_configs:
  - job_name: nvme
    scrape_interval: 60s
    scrape_timeout: 25s
    static_configs:
      - targets: ['nas.example.local:9998']
```

The Prometheus timeout must be greater than `--scrape-timeout`. Useful alert
expressions include `nvme_exporter_device_scrape_success == 0`,
`nvme_exporter_discovery_success == 0`, `nvme_exporter_devices == 0` when at
least one drive is expected, `critical_warning != 0`, and
`up{job="nvme"} == 0`.

## Grafana

Import `grafana.json` through
**Dashboards → New → Import → Upload dashboard JSON file**. Select the
**Prometheus** data source after importing, followed by the **Job**, **Instance**,
and **Device** filters. Prometheus must already scrape the exporter's `/metrics`
endpoint; the dashboard does not create a data source or change scrape settings.

The dashboard contains 24 panels covering exporter availability and diagnostics,
models and serial numbers, controller and sensor temperatures, endurance and
spare capacity, reads and writes, power-on time, power cycles, errors, and
temperature warnings. The **All SMART metrics** table automatically shows new
fields, while the **SMART metric** filter selects a field for its history chart.

Leave **SMART prefix** empty with the default exporter configuration. Enter
`nvme_` when the exporter runs with `--metric-prefix=nvme_`. Configure the real
scrape interval, such as `60s`, in the Prometheus data source so the command-rate
chart can calculate `$__rate_interval` correctly.

Raw `data_units_*` values are shown without conversion to bytes. SMART duration
fields use minutes, while `power_on_hours` uses hours. Missing values are not
replaced with zeroes.

## Options

| Flag | Default | Purpose |
| --- | --- | --- |
| `--listen-address` | `:9998` | HTTP listen address |
| `--nvme-path` | `nvme` | Path to the nvme-cli executable |
| `--sysfs-path` | `/sys/class/nvme` | NVMe controller class directory |
| `--dev-path` | `/dev` | Device node directory |
| `--metric-prefix` | empty | Optional SMART metric prefix |
| `--command-timeout` | `5s` | Timeout for each nvme-cli command |
| `--scrape-timeout` | `20s` | Total collection timeout, including time waiting for another scrape |

Example Compose configuration:

```yaml
    command: ['--metric-prefix=nvme_', '--command-timeout=5s']
```

At most four controllers are queried concurrently. Concurrent HTTP scrapes are
serialized. Diagnostics are written to stderr and can be viewed with
`docker logs nvme-exporter`. When changing the HTTP address or port, update the
Compose `ports` mapping and Docker health check as well.

## Development

Go 1.26 or newer is required. The image build runs tests before compilation.

```sh
go test ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/nvme-exporter .
docker build -t nvme-exporter:local .
docker run --rm --entrypoint /bin/sh -v "$PWD:/fixtures:ro" nvme-exporter:local /fixtures/testdata/smoke-test.sh
```

The tests use the provided SMART output and simulated controllers. They cover
dynamic fields, thousands separators, unit suffixes, hexadecimal and 128-bit
values, partition exclusion, device addition and removal, labels, failures, and
timeouts. Testing a physical NVMe device requires Linux or Synology with access
to the device nodes.

Command documentation:
[nvme smart-log](https://github.com/linux-nvme/nvme-cli/blob/v2.16/Documentation/nvme-smart-log.txt),
[nvme id-ctrl](https://github.com/linux-nvme/nvme-cli/blob/v2.16/Documentation/nvme-id-ctrl.txt).
