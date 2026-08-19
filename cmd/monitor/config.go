package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// fileConfig mirrors the CLI flags and can be loaded via -config so a
// monitor setup doesn't need to be retyped as flags every run. Any flag the
// user passes explicitly on the command line overrides the matching value
// here — see the explicit-flags check in main().
type fileConfig struct {
	URLs          []string          `json:"urls"`
	Timeout       flexDuration      `json:"timeout"`
	Interval      flexDuration      `json:"interval"`
	FailThreshold int               `json:"fail_threshold"`
	DB            string            `json:"db"`
	Webhook       string            `json:"webhook"`
	Listen        string            `json:"listen"`
	Headers       map[string]string `json:"headers"`
}

func loadConfig(path string) (fileConfig, error) {
	var cfg fileConfig

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("reading config file: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing config file: %w", err)
	}
	return cfg, nil
}

// flexDuration unmarshals from a JSON string like "30s" via
// time.ParseDuration, so config files can use human-friendly durations
// instead of raw nanosecond integers.
type flexDuration time.Duration

func (d *flexDuration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"30s\": %w", err)
	}
	if s == "" {
		*d = 0
		return nil
	}

	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = flexDuration(parsed)
	return nil
}
