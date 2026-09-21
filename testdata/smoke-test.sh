#!/bin/sh
# Run inside the final image, with the repository mounted at /fixtures:ro.
set -eu

nvme version
mkdir -p /tmp/nvme-fixture/nvme0 /tmp/nvme-fixture/nvme1
for device in nvme0 nvme1; do
  printf 'PM981a NVMe Samsung 512GB\n' > "/tmp/nvme-fixture/$device/model"
  printf 'SN-%s\n' "$device" > "/tmp/nvme-fixture/$device/serial"
done
cat > /tmp/fake-nvme <<'EOF'
#!/bin/sh
set -eu
test "$1" = smart-log
test "$3" = --output-format=normal
test "$LC_ALL" = C
cat /fixtures/testdata/smart-log.txt
EOF
chmod +x /tmp/fake-nvme
nvme-exporter --sysfs-path=/tmp/nvme-fixture --nvme-path=/tmp/fake-nvme &
exporter_pid=$!
trap 'kill "$exporter_pid" 2>/dev/null || true' EXIT
attempt=0
until wget -q -O /dev/null http://127.0.0.1:9998/healthz; do
  attempt=$((attempt + 1))
  test "$attempt" -lt 30
  sleep 0.1
done
wget -q -O /tmp/metrics http://127.0.0.1:9998/metrics
grep -Fx 'nvme_exporter_devices 2' /tmp/metrics
grep -Fx 'nvme_exporter_discovery_success 1' /tmp/metrics
grep -Fx 'host_write_commands{device="/dev/nvme0",model="PM981a NVMe Samsung 512GB",serial="SN-nvme0"} 236864267' /tmp/metrics
grep -Fx 'temperature_sensor_2{device="/dev/nvme1",model="PM981a NVMe Samsung 512GB",serial="SN-nvme1"} 71' /tmp/metrics
test "$(grep -c '^nvme_exporter_device_scrape_success.* 1$' /tmp/metrics)" -eq 2
kill "$exporter_pid"
wait "$exporter_pid"
trap - EXIT
printf 'Container smoke test passed\n'
