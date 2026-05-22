# Contributing to Blocks Network SDK

Thank you for your interest in contributing to Blocks Network SDK.

This repository contains the public Node SDK, Python SDK, CLI, MCP
server, schemas, and example agents. Contributions should keep those
surfaces consistent with each other.

## Getting Started

1. Fork and clone the repository.
2. Run `make setup` to install all dependencies.
3. Create a feature branch from `main`.
4. Make your changes with tests.
5. Run `make test` and `make lint` to verify.
6. Open a pull request.

## What Makes a Good Pull Request

- Keep the change focused and easy to review.
- Include a clear description of the problem and the fix.
- Add or update tests for SDK behavior changes.
- Update examples or docs when public behavior changes.
- Preserve Node/Python SDK parity unless the difference is intentional
  and documented.
- Do not commit secrets, `.env` files, generated credentials, local
  scratch files, or temporary artifacts.

## Local Checks

Run the smallest relevant check first:

```bash
make build
make test
make lint
```

Targeted checks:

```bash
npm test --workspace sdks/node

cd sdks/python
pip install -e ".[dev]"
pytest

cd cli
go test ./...
```

## Releases

Package releases are maintainer-only. They require repository write
access and registry credentials configured in GitHub Actions. See
[RELEASING.md](RELEASING.md) for the maintainer process.

Contributors do not need to run release commands.

## Code of Conduct

All contributors must follow the [Code of Conduct](CODE_OF_CONDUCT.md).

## Reporting Issues

Use GitHub Issues for bug reports and feature requests.

Do not open public issues for security vulnerabilities. Follow
[SECURITY.md](SECURITY.md) instead.

## License

By contributing, you agree that your contributions will be licensed
under the same license as this project.
