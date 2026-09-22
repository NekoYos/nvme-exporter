package main

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

var (
	invalidName     = regexp.MustCompile(`[^a-z0-9_]+`)
	numericValue    = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)
	temperatureName = regexp.MustCompile(`^temperature_sensor_[1-8]$`)
)

func metricName(name string) string {
	name = invalidName.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "_")
	if name != "" && name[0] >= '0' && name[0] <= '9' {
		name = "_" + name
	}
	return name
}

// Keep JSON numbers as text so 128-bit counters never pass through float64.
func parseNumber(value string) (string, bool) {
	return value, numericValue.MatchString(value)
}

func parseSMART(output string) (map[string]string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &fields); err != nil {
		return nil, fmt.Errorf("decode smart-log JSON: %w", err)
	}
	metrics := make(map[string]string)
	for key, raw := range fields {
		value := strings.TrimSpace(string(raw))
		// Accept numeric strings as well as JSON numbers for large counters.
		if strings.HasPrefix(value, `"`) {
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, fmt.Errorf("decode SMART field %q: %w", key, err)
			}
		}
		value, ok := parseNumber(value)
		if !ok {
			continue
		}
		name := metricName(key)
		if name == "" {
			continue
		}
		if _, exists := metrics[name]; exists {
			return nil, fmt.Errorf("duplicate SMART metric after normalization: %s", name)
		}
		if key == "temperature" || temperatureName.MatchString(key) {
			kelvin, ok := new(big.Int).SetString(value, 10)
			if !ok || kelvin.Sign() < 0 || kelvin.Cmp(big.NewInt(65535)) > 0 {
				return nil, fmt.Errorf("invalid Kelvin temperature for %s: %s", key, value)
			}
			// Zero means a temperature is not reported, not absolute zero.
			if kelvin.Sign() == 0 {
				continue
			}
			// Match nvme-cli's whole-degree Celsius presentation.
			value = kelvin.Sub(kelvin, big.NewInt(273)).String()
		}
		metrics[name] = value
	}
	if len(metrics) == 0 {
		return nil, fmt.Errorf("nvme smart-log returned no numeric fields")
	}
	return metrics, nil
}
