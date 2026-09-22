package main

import (
	"os"
	"testing"
)

func TestSMARTExample(t *testing.T) {
	data, err := os.ReadFile("testdata/smart-log.json")
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := parseSMART(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 24 {
		t.Fatalf("got %d metrics, want 24", len(metrics))
	}
	for name, want := range map[string]string{
		"temperature": "49", "avail_spare": "100", "spare_thresh": "50",
		"percent_used": "2", "data_units_read": "12162788",
		"host_write_commands": "236864267", "warning_temp_time": "0",
		"critical_comp_time": "0", "temperature_sensor_2": "71",
	} {
		if metrics[name] != want {
			t.Errorf("%s = %q, want %q", name, metrics[name], want)
		}
	}
	if _, exists := metrics["temperature_sensor_8"]; exists {
		t.Fatal("absent sensor was synthesized")
	}
}

func TestSMARTNumbersAndDynamicFields(t *testing.T) {
	metrics, err := parseSMART(`{
		"counter":340282366920938463463374607431768211455,
		"string_counter":"340282366920938463463374607431768211455",
		"New Vendor Field":42,
		"decimal":1.5,
		"exponent":1e3,
		"negative":-5,
		"zero":0,
		"unsupported":"N/A",
		"human":"49 C",
		"null":null,
		"boolean":true,
		"array":[1,2],
		"nested":{"value":42}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"counter":          "340282366920938463463374607431768211455",
		"string_counter":   "340282366920938463463374607431768211455",
		"new_vendor_field": "42", "decimal": "1.5", "exponent": "1e3", "negative": "-5", "zero": "0",
	}
	if len(metrics) != len(want) {
		t.Fatalf("unexpected metrics: %v", metrics)
	}
	for name, value := range want {
		if metrics[name] != value {
			t.Errorf("%s = %q, want %q", name, metrics[name], value)
		}
	}
}

func TestSMARTTemperatures(t *testing.T) {
	metrics, err := parseSMART(`{"temperature":273,"temperature_sensor_1":268,"temperature_sensor_2":0,"temperature_sensor_8":"344","warning_temp_time":10,"thm_temp1_total_time":60}`)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"temperature": "0", "temperature_sensor_1": "-5", "temperature_sensor_8": "71",
		"warning_temp_time": "10", "thm_temp1_total_time": "60",
	} {
		if metrics[name] != want {
			t.Errorf("%s = %q, want %q", name, metrics[name], want)
		}
	}
	if _, exists := metrics["temperature_sensor_2"]; exists {
		t.Fatal("zero Kelvin sensor should be omitted")
	}
}

func TestInvalidSMART(t *testing.T) {
	for _, output := range []string{
		"", "no SMART data", "{}", "null", "[]", "42",
		`{"temperature":322,}`, `{"counter":1} {"counter":2}`,
		`{"Field A":1,"field_a":2}`, `{"unsupported":"N/A"}`,
		`{"temperature":0}`, `{"temperature":-1}`, `{"temperature":65536}`,
		`{"temperature":322.5}`, `{"temperature_sensor_1":-1}`,
	} {
		t.Run(output, func(t *testing.T) {
			if metrics, err := parseSMART(output); err == nil {
				t.Fatalf("expected error, got %v", metrics)
			}
		})
	}
}
