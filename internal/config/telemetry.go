package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// TelemetryConfig holds the contents of .agentfactory/telemetry.json — where step
// records are exported to (issue #329). There is deliberately no "enabled" field:
// enablement is the .telemetry-gate file, so an operator can turn export off without
// editing or deleting their backend settings. Like models.json an absent file yields an
// empty config, never a not-found error.
//
// Header values are secret references and are never echoed — not by af telemetry
// status, and not by the validation errors below.
type TelemetryConfig struct {
	Endpoint           string            `json:"endpoint"`
	OTLPHTTPPathTraces string            `json:"otlp_http_path_traces"`
	Headers            map[string]string `json:"headers"`
	Protocol           string            `json:"protocol"`
	ExportTimeoutMS    int               `json:"export_timeout_ms"`
	ResourceAttrsExtra map[string]string `json:"resource_attributes_extra"`
}

const (
	// telemetryProtocolHTTPJSON is the only wire protocol this version speaks. New
	// values are additive, so an unrecognized one is a loud config error rather than a
	// silent fallback that would export nothing an operator could find.
	telemetryProtocolHTTPJSON = "http/json"

	// defaultExportTimeoutMS substitutes for an unset or non-positive timeout, matching
	// the way an unset dispatch interval is filled in.
	defaultExportTimeoutMS = 500

	// maxExportTimeoutMS rejects rather than fills, which is stricter than any other
	// numeric field in this package. Telemetry export happens inline in agent lifecycle
	// verbs, so a mistyped timeout does not degrade observability — it stalls the agent,
	// and load time is the last point where an operator still has the context to fix it.
	// The bound is deliberately generous: it catches an obviously wrong magnitude (a
	// stray trailing zero, or seconds typed where milliseconds were meant), and is not a
	// recommendation. Keeping export fast is the 500 ms default's job, not this ceiling's.
	maxExportTimeoutMS = 60000
)

// LoadTelemetryConfig loads and validates .agentfactory/telemetry.json. An absent file
// returns an empty config + nil error (NOT a not-found error), mirroring
// LoadModelsConfig: having no telemetry.json is the normal state until an operator
// provisions a backend, so every caller would otherwise have to special-case it.
//
// The factory root is a parameter and the loader reads no environment (ADR-004); the
// caller decides which root telemetry state belongs to.
func LoadTelemetryConfig(root string) (*TelemetryConfig, error) {
	path := TelemetryConfigPath(root)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &TelemetryConfig{}, nil
		}
		return nil, fmt.Errorf("reading telemetry config: %w", err)
	}
	var cfg TelemetryConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing telemetry config: %w", err)
	}
	if err := validateTelemetryConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// validateTelemetryConfig is pure and fills the timeout default in place. Unknown JSON
// fields are tolerated by construction — encoding/json ignores them by default, and
// that tolerance is what lets a newer af write a key this build does not know about
// without breaking it.
func validateTelemetryConfig(cfg *TelemetryConfig) error {
	if cfg.Endpoint != "" {
		u, err := url.Parse(cfg.Endpoint)
		// The Host check matters: url.Parse accepts both "garbage" and "http://",
		// neither of which is a reachable endpoint.
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%w: telemetry endpoint %q must start with http:// or https://", ErrInvalidType, cfg.Endpoint)
		}
	}

	if cfg.Protocol != "" && cfg.Protocol != telemetryProtocolHTTPJSON {
		return fmt.Errorf("%w: telemetry protocol %q is not supported (only %q)", ErrInvalidType, cfg.Protocol, telemetryProtocolHTTPJSON)
	}

	if cfg.ExportTimeoutMS > maxExportTimeoutMS {
		return fmt.Errorf("%w: telemetry export_timeout_ms %d exceeds the %d ms ceiling", ErrInvalidType, cfg.ExportTimeoutMS, maxExportTimeoutMS)
	}
	if cfg.ExportTimeoutMS <= 0 {
		cfg.ExportTimeoutMS = defaultExportTimeoutMS
	}

	for name, val := range cfg.Headers {
		if err := validateTelemetryHeader(name, val); err != nil {
			return err
		}
	}
	return nil
}

// validateTelemetryHeader shape-checks one header. It names the offending key and
// NEVER the value: a header value is either a literal credential or a reference to a
// secret file, and an error message is the easiest place to spill one into a terminal
// or a log. The file: reference rule matches the one models.json uses for auth tokens,
// but that helper reports the value verbatim, so the check is spelled out here rather
// than shared.
func validateTelemetryHeader(name, val string) error {
	if val == "" {
		return fmt.Errorf("%w: telemetry header %q has an empty value", ErrInvalidType, name)
	}
	if !isSecretRef(val) {
		return nil
	}
	path := strings.TrimPrefix(val, secretRefPrefix)
	if path == "" || strings.ContainsAny(path, secretRefShellMetacharacters) {
		return fmt.Errorf("%w: telemetry header %q has an invalid file: reference (value withheld); it must be a non-empty path with no shell metacharacters", ErrInvalidType, name)
	}
	return nil
}
