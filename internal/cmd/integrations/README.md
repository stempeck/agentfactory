# Embedded reference integrations

Each subdirectory `<name>/` here is a reference integration compiled into the `af` binary: a plugin
repository with an `af-integration.toml` manifest at its top level. `af plugin acquire <name>`
copies it into `.agentfactory/store/plugins/<name>/`, from where `af plugin install <name>` is the
consent step, exactly as for a cloned plugin repository. See [USING_PLUGINS.md](../../../USING_PLUGINS.md).

The directory is embedded with `//go:embed all:integrations` (`integrations_embed.go`), so dotfiles
are included. `TestEmbeddedIntegrationManifests` pins the embedded file set to `git ls-files` of
this directory: commit every file you add here, and leave nothing untracked in it.

The references:

- `aws-agents/`: the AWS Agent Toolkit plugin from its upstream repository, with a check that the AWS
  Knowledge MCP endpoint answers. Formula scope; no install step.
- `playwright/`: the Playwright MCP server, on the integration's own locked CLI and chromium build.
  Formula scope.
