package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stempeck/agentfactory/internal/config"
)

// The four ways a file: reference fails to resolve, kept distinguishable because the status
// surface reports the cause and cannot name the header. Collapsing them into one message tells an
// operator whose credential was REFUSED for escaping the factory root to go check that the file
// exists — which it does, and which is not the problem.
var (
	errSecretNoPath      = errors.New("its file: reference has no path")
	errSecretOutsideRoot = errors.New("its secret file resolves outside the factory root")
	errSecretUnreadable  = errors.New("its secret file could not be read")
	errSecretEmpty       = errors.New("its secret file is empty")
)

// telemetrySecretCause renders a resolution failure in the operator's terms for a surface that
// reports header COUNTS and never header identity, so the header name must not appear.
func telemetrySecretCause(err error) string {
	switch {
	case errors.Is(err, errSecretOutsideRoot):
		return "a configured credential resolves outside the factory root and was refused"
	case errors.Is(err, errSecretEmpty):
		return "a configured credential's secret file is empty"
	case errors.Is(err, errSecretNoPath):
		return "a configured credential's file: reference has no path"
	default:
		return "a configured credential could not be read from its secret file"
	}
}

// derefTelemetryHeaders resolves file: header references to their contents, for callers that
// are about to hand the configuration to an exporter.
//
// internal/telemetry refuses a header it has not been given the root to vet, and says so: the
// dereference belongs to the caller that knows the factory root. This is that caller's half.
// Without it the export path sends nothing at all — `af done` warns once per close and no
// af-plane span ever reaches the backend, so there are no step windows for the native events
// to join against.
//
// It is named for the ACTION rather than for the load on purpose. Of the seven sites that load a
// telemetry configuration, five sites are deref-permitted — the three export paths, the JSON
// status path, and the `usage` query command — one is the session launch path, which must NOT
// resolve anything — it hands the reference through verbatim so the pane shell reads the secret
// at exec time and the plaintext never lands in a tmux environment listing — and the seventh is
// the backend-liveness guard (`ensureTelemetryBackend`), which touches no header at all: it reads
// only the configured endpoint for a healthz probe, so it has nothing to deref and nothing to
// forbid. A helper called "loadResolvedTelemetryConfig" would invite exactly that regression,
// so the name states that dereferencing is a thing you do deliberately, at a moment of your
// choosing.
//
// Those numbers are checked against the tree rather than trusted: TestDerefRuleCommentRecordsItsOwnArithmetic
// counts both site sets by AST and fails if this paragraph disagrees with them, and
// TestDerefSites_ExactlyFourPermitted names the permitted set function by function. The check
// exists because this paragraph was already wrong once — it claimed a total one lower than the
// tree's, because a phase added a site and the sentence did not move with it. A rule that presents
// itself as the record is worse than no rule when it is stale, because it is believed.
//
// The input config is never mutated: callers hold a *config.TelemetryConfig from the loader and
// a copy keeps this function from reaching back into anything they still rely on.
func derefTelemetryHeaders(factoryRoot string, cfg config.TelemetryConfig) (config.TelemetryConfig, error) {
	if len(cfg.Headers) == 0 {
		return cfg, nil
	}

	resolved := make(map[string]string, len(cfg.Headers))
	for name, value := range cfg.Headers {
		if !strings.HasPrefix(value, secretPrefix) {
			resolved[name] = value
			continue
		}

		path, err := telemetrySecretPath(factoryRoot, value)
		if err != nil {
			return config.TelemetryConfig{}, fmt.Errorf("telemetry header %q: %w", name, err)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			// The key is named so an operator can find the header; the path and the value are
			// withheld, matching the package's rule that a header value never reaches a
			// terminal or a log — an operator-chosen path can itself be sensitive.
			return config.TelemetryConfig{}, fmt.Errorf(
				"telemetry header %q: %w (path withheld); "+
					"check the file exists and is readable", name, errSecretUnreadable)
		}

		// Command substitution strips trailing newlines, and the launch plane resolves this
		// same reference with $(cat …). Trimming only \r and \n keeps the two planes
		// byte-identical for a hand-written secret file that ends in one, without also eating
		// trailing spaces, which $(cat …) preserves. An untrimmed newline would make an
		// otherwise valid credential an invalid HTTP header value, failing at request-write
		// time — a different silent failure in place of the one being fixed.
		secret := strings.TrimRight(string(data), "\r\n")
		// Emptiness is judged after trimming ALL surrounding whitespace, while the value keeps only
		// its \r\n trimmed. A file holding spaces or a tab is as unusable as an empty one — it would
		// reach the backend as a malformed credential — but trimming spaces out of the value itself
		// would diverge from the launch plane, where $(cat …) preserves them.
		if strings.TrimSpace(secret) == "" {
			return config.TelemetryConfig{}, fmt.Errorf(
				"telemetry header %q: %w (path withheld); an empty "+
					"credential would be rejected by the backend as an authentication failure", name, errSecretEmpty)
		}
		resolved[name] = secret
	}

	out := cfg
	out.Headers = resolved
	return out, nil
}

// telemetrySecretPath resolves a file: reference the way the launch plane does — absolute paths
// as-is, relative paths against the factory root — and then bounds the result inside that root.
//
// The bound exists because of this change, not independently of it. Configuration validation
// rejects shell metacharacters in a secret path but permits `..` and absolute paths, and until
// now the export path never opened the file, so a traversing path was inert. Dereferencing it
// without a bound would turn a writable telemetry.json into an arbitrary-file-read whose
// contents are then sent to a configured endpoint.
//
// A path outside the root is refused rather than silently ignored: an operator who wrote it
// meant something by it, and a credential that quietly fails to load is the failure mode this
// whole thread is about.
func telemetrySecretPath(factoryRoot, ref string) (string, error) {
	raw := strings.TrimPrefix(ref, secretPrefix)
	if raw == "" {
		return "", errSecretNoPath
	}

	path := raw
	if !filepath.IsAbs(path) {
		path = filepath.Join(factoryRoot, path)
	}
	path = filepath.Clean(path)

	root := filepath.Clean(factoryRoot)
	if !withinRoot(root, path) {
		return "", fmt.Errorf("%w (path withheld)", errSecretOutsideRoot)
	}

	// A lexical check alone is not a containment boundary: a symlink inside the factory can point
	// anywhere, so `.agentfactory/secrets/x -> /etc/shadow` satisfies every string comparison above
	// and the contents would then be POSTed to the configured endpoint. Resolving the link is what
	// makes the bound mean what its name says.
	//
	// EvalSymlinks fails when the path does not exist, which is not a containment failure — it is
	// the missing-file case the caller reports with its own message. So an error here falls through
	// to the lexical result and the subsequent read produces the honest error.
	if realPath, err := filepath.EvalSymlinks(path); err == nil {
		realRoot, rootErr := filepath.EvalSymlinks(root)
		if rootErr != nil {
			realRoot = root
		}
		if !withinRoot(realRoot, realPath) {
			return "", fmt.Errorf("%w through a link (path withheld)", errSecretOutsideRoot)
		}
	}
	return path, nil
}

// withinRoot reports whether path is root itself or sits beneath it. Comparing the relative path
// rather than string prefixes avoids the classic near-miss where "/factory-evil" passes a prefix
// test against "/factory".
func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
