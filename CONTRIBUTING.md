# Contributing to Cloud Tasks Emulator

Thank you for your interest in contributing!

## Getting Started

1. Fork the repository
2. Clone your fork: `git clone https://github.com/YOUR_USERNAME/cloud-tasks-emulator.git`
3. Create a branch: `git checkout -b feature/your-feature`
4. Make your changes
5. Run tests: `make test`
6. Run lint: `golangci-lint run`
7. Commit with clear messages
8. Push and open a Pull Request

## Development Setup

```bash
make run          # Start the emulator
make test         # Run tests
make build        # Build binary
make docker       # Build Docker image
make install-hooks      # Install pre-commit hooks (runs fmt, lint, test on commit)
make release-snapshot        # Test release (Linux only - requires native Go)
make release-snapshot-docker # Test release in Docker (use on Mac/Windows - matches CI)
```

### Pre-commit Hooks

To run `go fmt`, `golangci-lint`, and `go test` automatically before each commit:

```bash
# Install pre-commit (one-time): pip install pre-commit  OR  brew install pre-commit
make install-hooks
```

## Code Style

- Follow standard Go conventions
- Run `go fmt` and `golangci-lint run` before committing
- Keep dependencies minimal
- Add tests for new functionality

## Pull Request Process

1. Ensure all tests pass
2. Update README if adding features
3. Keep PRs focused and reasonably sized
4. Describe your changes clearly in the PR description

## Reporting Bugs

Open an issue with:

- Go version and OS
- Steps to reproduce
- Expected vs actual behavior
- Relevant logs if applicable
