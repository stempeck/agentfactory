package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// IsLoopbackEndpoint reports whether baseURL points at a loopback host (localhost,
// the 127.0.0.0/8 range, or ::1). It is the ONE loopback classifier for the root
// module: the models.json validator (this file), the launch preflight,
// and the legacy auth_token guard all share it instead of
// re-deriving the rule. The web module has its own copy
// (web/internal/server/server.go hostnameIsLoopback) that cannot cross the
// separate-module boundary, so this is a fresh implementation, not a re-export.
//
// The explicit string switch must precede net.ParseIP because ParseIP("localhost")
// is nil — the seeded lmstudio profile (http://localhost:1234) would otherwise be
// misclassified as non-loopback. A parse failure or empty host yields false
// (fail-closed: an unparseable base_url must not silently exempt a literal token
// from the credential guard).
func IsLoopbackEndpoint(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := u.Hostname() // strips :port and IPv6 brackets
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// secretRefPrefix marks an ANTHROPIC_AUTH_TOKEN value as a reference to a secret
// file (dereferenced at launch time in Phase 2) rather than a literal token.
const secretRefPrefix = "file:"

// secretRefShellMetacharacters are rejected in a file: reference path so a token
// value can never smuggle a command into the shell launch line the session layer
// builds. It is a denylist (mirroring internal/cmd/containment.go's ContainsAny
// mechanism) widened to command separators and whitespace; a legitimate secret
// path (e.g. .agentfactory/secrets/x.key) needs none of these, only alphanumerics
// and / . - _.
const secretRefShellMetacharacters = " \t\r\n;|&$`<>(){}[]!*?~\"'\\"

// isSecretRef reports whether tok uses the file: reference convention.
func isSecretRef(tok string) bool {
	return strings.HasPrefix(tok, secretRefPrefix)
}

// validateSecretRefShape is a PURE shape check — it never stats the referenced
// file, so validateModelsConfig stays pure and deterministic. A file: reference
// must carry a non-empty path free of shell metacharacters.
func validateSecretRefShape(name, tok string) error {
	path := strings.TrimPrefix(tok, secretRefPrefix)
	if path == "" || strings.ContainsAny(path, secretRefShellMetacharacters) {
		return fmt.Errorf("%w: model %q has an invalid %s %q: a file: reference must be a non-empty path with no shell metacharacters", ErrInvalidType, name, envAuthToken, tok)
	}
	return nil
}

// looksLikeCredential is the narrow heuristic behind the literal-credential guard:
// it flags only an obvious provider-key shape (sk- prefix), so dummy literals like
// "tok" or lmstudio's "lm-studio" stay legal. It is intentionally conservative —
// the file: convention plus the secret-file dereference is the actual guarantee;
// this guard is a best-effort backstop that turns the most likely operator mistake
// (a real key pasted onto a remote endpoint) into a loud config error. Do NOT
// broaden it: a wider heuristic would reject the legitimate literal "tok" fixture.
func looksLikeCredential(tok string) bool {
	return strings.HasPrefix(tok, "sk-")
}
