# NVMe Prometheus Exporter

A Go exporter for Linux and Synology DSM. The Alpine-based image includes
`nvme-cli` pinned with `nvme-cli~2.16` (allowing Alpine package revisions), so the utility does not need to be installed on the NAS. The Go
application has no third-party dependencies.

On every `/metrics` request, the exporter enumerates controllers matching
`/sys/class/nvme/nvmeN` and runs
`nvme smart-log /dev/nvmeN --output-format=json`. Namespaces and partitions do
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

SMART metric names are generated dynamically from the top-level JSON keys. Names are converted to lowercase,
and spaces or other characters that are invalid in Prometheus metric names are
replaced with `_`. No prefix is used by default; an optional prefix can be set
with `--metric-prefix=nvme_`.

```prometheus
temperature{device="/dev/nvme0",model="PM981a NVMe Samsung 512GB",serial="S4GTNF0MA61796"} 49
host_write_commands{device="/dev/nvme0",model="PM981a NVMe Samsung 512GB",serial="S4GTNF0MA61796"} 236864267
warning_temp_time{device="/dev/nvme0",model="PM981a NVMe Samsung 512GB",serial="S4GTNF0MA61796"} 0
temperature_sensor_2{device="/dev/nvme0",model="PM981a NVMe Samsung 512GB",serial="S4GTNF0MA61796"} 71
```

The parser reads numeric JSON fields and numeric strings without converting them
to float64, preserving large integer counters while generating the response.
Other value types and nonnumeric strings are skipped. Invalid JSON or a response
without usable numeric fields causes collection for that device to fail.

The `temperature` and `temperature_sensor_1` through `temperature_sensor_8`
fields are converted from Kelvin to whole degrees Celsius by subtracting 273,
matching nvme-cli's text presentation. A zero Kelvin value is treated as
unreported and omitted. Sensors absent from JSON are not synthesized.

All other units remain as reported by nvme-cli: percentages are not divided by
100, and `data_units_*` values are not converted to bytes. Prometheus itself
stores numbers as float64.

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

Raw `data_units_*` values are shown without conversion to bytes. `controller_busy_time`, `warning_temp_time`, and `critical_comp_time`
use minutes; `thm_temp1_total_time` and `thm_temp2_total_time` use seconds;
`power_on_hours` uses hours. Missing values are not
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

## GitHub Actions and Docker Hub

`.github/workflows/ci.yml` runs formatting checks, Go tests with the race
detector, `go vet`, Docker builds, and container smoke tests on native
`amd64` and `arm64` runners. Pull requests run these checks without Docker Hub
credentials. `.github/workflows/publish.yml` calls the same checks before
publishing a multi-platform image to `nekoyos/nvme-exporter`.

Configure these repository secrets under **Settings > Secrets and variables >
Actions**:

- `DOCKERHUB_USERNAME`: the Docker Hub account with write access to the repository.
- `DOCKERHUB_TOKEN`: a Docker Hub access token with read/write access.

The Docker Hub repository must exist. Native ARM runners must be available to
the GitHub repository.

| Event | Published tags |
| --- | --- |
| Pull request | None |
| Push to `main` | `edge`, `sha-<full-commit>` |
| Push tag `v1.2.3` | `1.2.3`, `1.2`, `1`, `latest` |
| Push tag `v0.2.3` | `0.2.3`, `0.2`, `latest` (no `0` alias) |

Only stable `vX.Y.Z` tags are accepted; prereleases are rejected. Release tags
should point to reviewed commits from `main`. Publish releases in version order:
publishing or rerunning an older release also moves `latest` and its rolling
version aliases. Treat version tags as immutable and use an image digest when
deployments require immutable content.

Publication includes OCI labels, SBOM and provenance attestations. BuildKit
caches are reused between checks and publication. Publishing performs a
multi-platform build after the per-platform smoke checks; Dockerfile tests also
run during this build when their layer is not cached.

For example, after merging a release commit into `main`:

```sh
git tag v0.2.0
git push origin v0.2.0
```

Pushing these workflow files activates automation; no manual Docker Hub push is
needed. Protect `main` and release tags with repository rulesets to restrict who
can publish. Actions are pinned by commit SHA and need periodic updates.

## Development

Go 1.26 or newer is required. The image build runs tests before compilation.

```sh
go test ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/nvme-exporter .
docker build -t nvme-exporter:local .
docker run --rm --entrypoint /bin/sh -v "$PWD:/fixtures:ro" nvme-exporter:local /fixtures/testdata/smoke-test.sh
```

The tests use a representative nvme-cli 2.16 JSON fixture and simulated
controllers. They cover dynamic fields, Kelvin conversion, missing sensors,
128-bit counters, malformed JSON, partition exclusion, device addition and
removal, labels, failures, and timeouts. Testing a physical NVMe device requires Linux or Synology with access
to the device nodes.

Command documentation:
[nvme smart-log](https://github.com/linux-nvme/nvme-cli/blob/v2.16/Documentation/nvme-smart-log.txt),
[nvme id-ctrl](https://github.com/linux-nvme/nvme-cli/blob/v2.16/Documentation/nvme-id-ctrl.txt).
