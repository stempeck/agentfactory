package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var buildSourceRoot string
var envSourceRoot string

// SetBuildSourceRoot stores the build-time embedded source root path.
func SetBuildSourceRoot(root string) {
	buildSourceRoot = root
}

// SetEnvSourceRoot stores the AF_SOURCE_ROOT value read at the CLI boundary.
func SetEnvSourceRoot(root string) {
	envSourceRoot = root
}

// ResolveSourceRoot determines the agentfactory source tree location using a
// three-tier strategy:
//  1. Check if factoryRoot itself is the source tree (self-hosted case)
//  2. Check the build-time embedded path
//  3. Check the AF_SOURCE_ROOT environment variable
//
// All paths are validated via go.mod module line check.
func ResolveSourceRoot(factoryRoot string) (string, error) {
	if isAgentFactorySourceTree(factoryRoot) {
		return factoryRoot, nil
	}

	if buildSourceRoot != "" {
		if isAgentFactorySourceTree(buildSourceRoot) {
			return buildSourceRoot, nil
		}
		return "", fmt.Errorf("build-time source root %q is not an agentfactory source tree (go.mod check failed)", buildSourceRoot)
	}

	if envSourceRoot != "" {
		if isAgentFactorySourceTree(envSourceRoot) {
			return envSourceRoot, nil
		}
		return "", fmt.Errorf("AF_SOURCE_ROOT=%q is not an agentfactory source tree (go.mod check failed)", envSourceRoot)
	}

	return "", fmt.Errorf("cannot resolve agentfactory source root: factory root %q is not the source tree, no build-time root set, and AF_SOURCE_ROOT is not set.\nFix: set AF_SOURCE_ROOT to the agentfactory source checkout", factoryRoot)
}

// isAgentFactorySourceTree checks if dir contains a go.mod with the agentfactory module path.
func isAgentFactorySourceTree(dir string) bool {
	f, err := os.Open(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "module ") {
			return strings.Contains(line, "agentfactory")
		}
	}
	return false
}
