package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

var configFingerprintCmd = &cobra.Command{
	Use:   "fingerprint",
	Short: "Print a digest of the config schema this binary speaks",
	Long: `Print a SHA-256 digest of the canonical config schema as JSON on stdout.

The digest is recomputed in memory from the canonical structs on every invocation, so it
describes the schema of the BINARY YOU ARE RUNNING rather than a constant baked in at build
time. A consumer that mirrors these structs — the web console mirrors none of them precisely
so that it need not, but its committed fixtures pin the shape — can compare the digest it was
built against with the one af-core reports and tell an operator their two halves disagree,
instead of silently dropping fields.

Like the other --json read commands, this always exits 0 and a consumer branches on ` + "`state`" + `.`,
	RunE: runConfigFingerprint,
}

func init() {
	configFingerprintCmd.Flags().Bool("json", true, "Emit JSON output (currently the only supported format)")
	configCmd.AddCommand(configFingerprintCmd)
}

// configFingerprintJSON is the success DTO. Carrying `state` on a SUCCESS payload diverges from
// the house idiom, where `.state` discriminates errors only and success payloads omit it (see
// dispatchStatusJSON and formulaShowOutput). The divergence is deliberate and follows
// telemetryStateJSON: this verb always exits 0, so `state` is the ONLY channel a consumer has
// for telling "here is the schema" from "I could not compute one" — and a consumer that has to
// infer that from whether a field is empty will eventually infer it wrong.
type configFingerprintJSON struct {
	State       string `json:"state"`
	Fingerprint string `json:"fingerprint"`
}

func runConfigFingerprint(cmd *cobra.Command, _ []string) error {
	// No factory root is resolved and no file is read: the schema is a property of the binary,
	// not of any factory, so this verb answers identically from anywhere — which is what lets a
	// consumer call it before it knows whether the operator's cwd is a factory at all.
	fingerprint, err := config.SchemaFingerprint()
	if err != nil {
		return emitConfigFingerprintError(cmd, err)
	}

	data, err := json.Marshal(configFingerprintJSON{State: "ok", Fingerprint: fingerprint})
	if err != nil {
		return emitConfigFingerprintError(cmd, err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return nil
}

// emitConfigFingerprintError reports failure IN the payload and still exits 0, mirroring
// emitDispatchStatusError (dispatch.go). A read verb that exits non-zero forces every caller to
// tell "this af cannot describe its schema" apart from "this af has no such command", and those
// need different responses.
func emitConfigFingerprintError(cmd *cobra.Command, e error) error {
	data, err := json.Marshal(stepErrorOutput{State: "error", Error: e.Error()})
	if err != nil {
		fmt.Fprintln(cmd.OutOrStdout(), `{"state":"error","error":"json marshal failed"}`)
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return nil
}
