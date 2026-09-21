package main

import (
	"bufio"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

var (
	invalidName  = regexp.MustCompile(`[^a-z0-9_]+`)
	numericValue = regexp.MustCompile(`^[+-]?(?:0[xX][0-9a-fA-F]+|[0-9][0-9,]*(?:\.[0-9]+)?)`)
)

func metricName(name string) string {
	name = invalidName.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "_")
	if name != "" && name[0] >= '0' && name[0] <= '9' {
		name = "_" + name
	}
	return name
}

// Parse only the leading number: modern nvme-cli can append both human-readable
// byte counts and an alternative temperature unit to the same value.
func parseNumber(value string) (string, bool) {
	token := numericValue.FindString(strings.TrimSpace(value))
	if token == "" {
		return "", false
	}
	token = strings.ReplaceAll(token, ",", "")
	if strings.ContainsAny(token, "xX") {
		number, ok := new(big.Int).SetString(token, 0)
		if !ok {
			return "", false
		}
		return number.String(), true
	}
	// Normalize leading zeroes without losing precision in 128-bit SMART counters.
	if !strings.Contains(token, ".") {
		number, ok := new(big.Int).SetString(token, 10)
		if !ok {
			return "", false
		}
		return number.String(), true
	}
	return token, true
}

func parseSMART(output string) (map[string]string, error) {
	metrics := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), ":")
		if !found || strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), "smart log for") {
			continue
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
		metrics[name] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(metrics) == 0 {
		return nil, fmt.Errorf("nvme smart-log returned no numeric fields")
	}
	return metrics, nil
}
