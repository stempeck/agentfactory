package cmd

import "testing"

// fakeCmdTmux is a no-op cmdTmux double proving the newCmdTmux seam is a
// reassignable var and that the full cmdTmux surface (including the Phase-4
// methods) is implementable by a test double.
type fakeCmdTmux struct{}

func (fakeCmdTmux) IsAvailable() bool                                       { return false }
func (fakeCmdTmux) NewSession(name, workDir string) error                  { return nil }
func (fakeCmdTmux) HasSession(name string) (bool, error)                   { return false, nil }
func (fakeCmdTmux) KillSession(name string) error                          { return nil }
func (fakeCmdTmux) SendKeys(session, keys string) error                    { return nil }
func (fakeCmdTmux) SendKeysDelayed(session, keys string, delayMs int) error { return nil }
func (fakeCmdTmux) GetPaneCommand(session string) (string, error)          { return "", nil }
func (fakeCmdTmux) IsAgentRunning(session string, expectedPaneCommands ...string) bool {
	return false
}
func (fakeCmdTmux) SetEnvironment(session, key, value string) error { return nil }

func TestNewCmdTmuxSeam(t *testing.T) {
	orig := newCmdTmux
	defer func() { newCmdTmux = orig }()

	newCmdTmux = func() cmdTmux { return fakeCmdTmux{} }
	got := newCmdTmux()
	if _, ok := got.(fakeCmdTmux); !ok {
		t.Fatalf("newCmdTmux() did not return injected fakeCmdTmux, got %T", got)
	}
}
