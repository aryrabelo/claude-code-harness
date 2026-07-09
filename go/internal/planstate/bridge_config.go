package planstate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PlanqueueConfig is the opt-in bridge configuration from harness.toml:
//
//	[planqueue]
//	enabled = true
//	path = "~/.planqueue/backlog.db"   # optional; default shown
//
// Absent section or enabled=false means the bridge stays not-configured.
type PlanqueueConfig struct {
	Enabled bool
	Path    string
}

var (
	rePqSection = regexp.MustCompile(`(?ms)^\[planqueue\]\s*$(.*?)(^\[|\z)`)
	rePqEnabled = regexp.MustCompile(`(?m)^\s*enabled\s*=\s*(true|false)`)
	rePqPath    = regexp.MustCompile(`(?m)^\s*path\s*=\s*"([^"]+)"`)
)

// LoadPlanqueueConfig reads the [planqueue] section from projectRoot/harness.toml.
// A minimal section scanner is used deliberately — the harness has no TOML
// dependency in Go and this section is two flat keys.
func LoadPlanqueueConfig(projectRoot string) PlanqueueConfig {
	cfg := PlanqueueConfig{}
	data, err := os.ReadFile(filepath.Join(projectRoot, "harness.toml"))
	if err != nil {
		return cfg
	}
	m := rePqSection.FindStringSubmatch(string(data))
	if m == nil {
		return cfg
	}
	body := m[1]
	if e := rePqEnabled.FindStringSubmatch(body); e != nil {
		cfg.Enabled = e[1] == "true"
	}
	if p := rePqPath.FindStringSubmatch(body); p != nil {
		cfg.Path = p[1]
	}
	if cfg.Path == "" {
		cfg.Path = "~/.planqueue/backlog.db"
	}
	if strings.HasPrefix(cfg.Path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			cfg.Path = filepath.Join(home, cfg.Path[2:])
		}
	}
	return cfg
}
