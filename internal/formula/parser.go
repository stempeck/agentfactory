package formula

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// ParseFile reads and parses a formula.toml file.
func ParseFile(path string) (*Formula, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is from trusted formula directory
	if err != nil {
		return nil, fmt.Errorf("reading formula file: %w", err)
	}
	return Parse(data)
}

// Parse parses formula.toml content from bytes.
func Parse(data []byte) (*Formula, error) {
	var f Formula
	if _, err := toml.Decode(string(data), &f); err != nil {
		return nil, fmt.Errorf("parsing TOML: %w", err)
	}

	// Infer type from content if not explicitly set
	f.inferType()

	if err := f.Validate(); err != nil {
		return nil, err
	}

	return &f, nil
}

// inferType sets the formula type based on content when not explicitly set.
func (f *Formula) inferType() {
	if f.Type != "" {
		return
	}

	if len(f.Steps) > 0 {
		f.Type = TypeWorkflow
	} else if len(f.Legs) > 0 {
		f.Type = TypeConvoy
	} else if len(f.Template) > 0 {
		f.Type = TypeExpansion
	} else if len(f.Aspects) > 0 {
		f.Type = TypeAspect
	}
}
