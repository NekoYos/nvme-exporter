package main

import (
	"os"
	"testing"
)

func TestSMARTExample(t *testing.T) {
	data, err := os.ReadFile("testdata/smart-log.txt")
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := parseSMART(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 25 {
		t.Fatalf("got %d metrics, want 25", len(metrics))
	}
	for name, want := range map[string]string{
		"temperature": "49", "available_spare": "100", "data_units_read": "12162788",
		"host_write_commands": "236864267", "warning_temperature_time": "0",
		"critical_composite_temperature_time": "0", "temperature_sensor_2": "71",
		"temperature_sensor_8": "0",
	} {
		if metrics[name] != want {
			t.Errorf("%s = %q, want %q", name, metrics[name], want)
		}
	}
}

func TestNumericFormats(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		ok          bool
	}{
		{"236,864,267", "236864267", true},
		{"49 °C (322 Kelvin)", "49", true},
		{"12,162,788 [6.22 TB]", "12162788", true},
		{"100%", "100", true}, {"0x0a", "10", true},
		{"-5 C", "-5", true}, {"1.5 seconds", "1.5", true},
		{"00049", "49", true},
		{"340,282,366,920,938,463,463,374,607,431,768,211,455", "340282366920938463463374607431768211455", true},
		{"N/A", "", false}, {"", "", false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, ok := parseNumber(tc.input)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestDynamicFieldsAndInvalidOutput(t *testing.T) {
	metrics, err := parseSMART("New Vendor Field : 42%\nTemperature Sensor 12 : 39 C\nunsupported : N/A\n")
	if err != nil || metrics["new_vendor_field"] != "42" || metrics["temperature_sensor_12"] != "39" || len(metrics) != 2 {
		t.Fatalf("unexpected result: %v, %v", metrics, err)
	}
	for _, output := range []string{"", "Smart Log for NVME device:nvme1 namespace-id:ffffffff", "Field A: 1\nfield_a: 2"} {
		if _, err := parseSMART(output); err == nil {
			t.Fatalf("expected error for %q", output)
		}
	}
}
