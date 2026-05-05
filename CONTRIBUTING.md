# Contributing to agentfactory

Thanks for your interest in contributing! This document explains how to contribute and what to expect from the contribution process.

## License and Contributor License Agreement (CLA)

This project is licensed under **GNU Affero General Public License v3.0 (AGPL-3.0)**. By contributing to this project, you agree that your contributions will be licensed under AGPL-3.0 to the public.

Additionally, **all contributors must sign a Contributor License Agreement (CLA)** before their contributions can be merged. The CLA grants the project maintainer the right to relicense contributions, including under commercial terms. This allows the project to:

- Offer commercial licenses to organizations that cannot accept AGPL-3.0 obligations
- Preserve flexibility for the project's long-term direction

You retain copyright to your contributions. The CLA simply grants the maintainer broad licensing rights alongside your retained ownership.

### How CLA signing works

When you open your first pull request, the CLA Assistant bot will automatically post a comment with a link to sign the CLA. You sign once, and it covers all future contributions to this project. Pull requests cannot be merged until the CLA is signed.

If you have questions or concerns about the CLA, please open an issue before contributing significant work.

## Commercial Licensing

If your intended use of agentfactory falls outside what AGPL-3.0 permits — for example, embedding it in a proprietary product, integrating it into a commercial AI/developer tool offering, or providing it as a hosted service without releasing your full stack under AGPL-3.0 — contact **licensing@factoryofagents.com** to discuss a commercial license.

## Reporting Issues

If you use [Claude Code](https://claude.ai/claude-code), run `/github-issue` within context to create an issue. It investigates the codebase, maps affected files and data flow, and opens a well-structured issue with the context needed to act on it.

Otherwise, before opening an issue:

1. Search existing issues to avoid duplicates
2. Check the documentation and README
3. Verify the issue reproduces on the latest version

When opening an issue, please include:

- A clear, descriptive title
- Steps to reproduce (for bugs)
- Expected vs actual behavior
- Your environment (OS, Go version, agentfactory version)
- Relevant logs or error messages

## Submitting Pull Requests

### Before you start

For non-trivial changes, please open an issue first to discuss the approach. This avoids wasted work if the change does not fit the project's direction.

### Pull request workflow

1. Fork the repository and create a feature branch from `main`
2. Make your changes in focused, logical commits
3. Add or update tests for any behavioral changes
4. Run the test suite locally (`make test`) and confirm it passes
5. Update documentation if your change affects user-facing behavior
6. Open a pull request against `main` with a clear description of the change
7. Sign the CLA when prompted by the bot (first-time contributors)
8. Respond to review feedback

### Commit guidelines

- Write clear commit messages explaining the why, not just the what
- Keep commits focused — one logical change per commit when practical
- Reference relevant issue numbers in commit messages or PR descriptions

### Code style

- Match the existing style of surrounding code
- Run any configured linters or formatters before submitting
- Avoid unrelated formatting changes in functional PRs

## Development Setup

### Prerequisites

- Go 1.24+
- Python 3.12 (required by the MCP issue-store server)
- jq

### Build and test

```bash
git clone https://github.com/stempeck/agentfactory.git
cd agentfactory
make build     # builds the af binary (includes formula drift check)
make test      # runs unit tests
make install   # installs af to ~/.local/bin
```

If you modify formula TOML files under `internal/cmd/install_formulas/`, run `make sync-formulas` before `make build` to update the mirror copy.

## Code of Conduct

Be respectful, constructive, and patient in all project interactions. Harassment, personal attacks, and discriminatory behavior are not tolerated. Maintainers reserve the right to remove contributions or block contributors who violate these standards.

## Questions

For questions that are not bug reports or feature requests, open an issue with the "question" label.

Thank you for contributing to agentfactory.
