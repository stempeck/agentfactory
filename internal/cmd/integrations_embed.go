package cmd

import "embed"

// embeddedIntegrations holds the reference integrations shipped inside the af binary
// (internal/cmd/integrations/<name>/, design 695 K4); af plugin acquire materialises one into
// store/plugins/<name>/. The all: prefix keeps dotfiles such as .claude-plugin/ that a plugin
// dir needs; README.md keeps the embed non-empty, because an empty embed dir fails go build.
//
//go:embed all:integrations
var embeddedIntegrations embed.FS
