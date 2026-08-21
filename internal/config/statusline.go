package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/stempeck/agentfactory/internal/fsutil"
)

// StatuslineConfig holds the contents of .agentfactory/statusline.json — the ordered
// list of elements the Claude Code session statusline renders (issue #591). Like
// startup.json, an ABSENT file yields the populated default (not an empty list and not a
// not-found error): every later phase reads a config, so the absent-file case must be a
// usable default rather than a special case each caller re-handles.
//
// Elements is an ORDERED list, not a set — render order is the operator's choice, edited with
// `af config statusline set` and read back with `af config statusline get`. An empty (non-nil)
// list is a valid config that renders nothing; only an absent FILE yields the default (see
// LoadStatuslineConfig). Unknown JSON keys are tolerated on the LOAD path (additive evolution,
// telemetry.json posture) but rejected by `set`, where a missing "elements" key would otherwise
// blank the statusline silently. There is deliberately no version field.
//
// Color is a POINTER so that an absent key means ON. A plain bool would make the zero value
// false, and a fresh factory with no statusline.json would silently render without color —
// the opposite of the default the requirement asks for (api.md A2.1). Nothing normalizes it
// into the struct: filling it during validation would write a value the operator never typed
// back out through `af config statusline get`, so the absent⇒ON rule has exactly one home,
// ColorEnabled.
type StatuslineConfig struct {
	Elements []string `json:"elements"`
	Color    *bool    `json:"color,omitempty"`
}

// ColorEnabled reports whether ANSI color should be rendered. Absent (nil) means ON.
func (c *StatuslineConfig) ColorEnabled() bool {
	return c.Color == nil || *c.Color
}

// statuslineElementNames is the SINGLE canonical source of every valid statusline element name,
// in a stable order. The whitelist (validStatuslineElements) and the human-facing "valid: ..."
// list in validateStatuslineConfig's error are both DERIVED from it, so adding an element means
// editing ONE list on the config side rather than the several F6/T5 flagged (PR #595 T5/F6).
// render.go's lineOf + renderElement switch stay per-element by necessity — a new element there is
// genuine code, not a name copy (decisions.md D6 logs that STAY); TestFableIncr_T5_EveryValidElementRenders
// guards that they never fall out of sync with this list.
var statuslineElementNames = []string{"model", "dir", "branch", "diff", "elapsed", "context", "session", "daily"}

// validStatuslineElements is the whitelist validateStatuslineConfig checks against, DERIVED from
// statuslineElementNames so the two can never drift. Every valid element is also a default one —
// the roster and the default are the same list (see DefaultStatuslineElements).
var validStatuslineElements = func() map[string]bool {
	m := make(map[string]bool, len(statuslineElementNames))
	for _, name := range statuslineElementNames {
		m[name] = true
	}
	return m
}()

// StatuslineElementNames returns a copy of the canonical valid-element list. The render layer uses
// it to prove every valid element is wired into lineOf + renderElement, closing the last F6/T5
// drift path across the config→statusline package boundary (PR #595 T5/F6).
func StatuslineElementNames() []string {
	out := make([]string, len(statuslineElementNames))
	copy(out, statuslineElementNames)
	return out
}

// DefaultStatuslineElements returns a copy of the default render order, which IS the canonical
// roster: the watchdog strips sentinel-marked statusline lines before hashing a pane
// (internal/cmd/watchdog.go), so no element can reset its silence hash and none needs holding
// back. Exported so tests and the docs guard DERIVE the default instead of re-declaring it —
// the same drift path PR #595 T5/F6 closed for the roster itself.
func DefaultStatuslineElements() []string {
	return StatuslineElementNames()
}

// defaultStatuslineConfig returns a FRESH copy of the default element list on each call,
// so a caller mutating the returned slice can never corrupt the package-level default
// (mirrors how defaultStartupConfig builds a new struct each call).
func defaultStatuslineConfig() *StatuslineConfig {
	return &StatuslineConfig{Elements: DefaultStatuslineElements()}
}

// LoadStatuslineConfig loads and validates .agentfactory/statusline.json. An absent file
// returns the populated default + nil error (mirroring LoadStartupConfig, NOT
// LoadTelemetryConfig's empty-struct return). The factory root is a parameter and no
// environment is read (ADR-004).
func LoadStatuslineConfig(root string) (*StatuslineConfig, error) {
	path := StatuslineConfigPath(root)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultStatuslineConfig(), nil
		}
		return nil, fmt.Errorf("reading statusline config: %w", err)
	}
	var cfg StatuslineConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing statusline config: %w", err)
	}
	if err := validateStatuslineConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SaveStatuslineConfig validates then atomically writes the statusline config to path via
// fsutil.WriteFileAtomic. It does not assume the file pre-exists — the absent-file default
// lives in Load. Mirrors SaveStartupConfig.
func SaveStatuslineConfig(path string, cfg *StatuslineConfig) error {
	if err := validateStatuslineConfig(cfg); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling statusline config: %w", err)
	}
	data = append(data, '\n')
	return fsutil.WriteFileAtomic(path, data, 0644)
}

// validateStatuslineConfig rejects any element not on the whitelist. An empty (or nil)
// list is valid — it renders nothing. The error wraps ErrInvalidType (matching the whole
// config package's idiom, so errors.Is holds) and carries the design-doc's
// "statusline elements:" framing plus the full 8-name valid list.
func validateStatuslineConfig(cfg *StatuslineConfig) error {
	for _, e := range cfg.Elements {
		if !validStatuslineElements[e] {
			return fmt.Errorf("%w: statusline elements: unknown element %q (valid: %s)", ErrInvalidType, e, strings.Join(statuslineElementNames, ", "))
		}
	}
	return nil
}
