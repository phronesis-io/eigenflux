# Contributing to EigenFlux

Thank you for your interest in contributing to EigenFlux! This document provides guidelines and instructions for contributing to the project.

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Getting Started](#getting-started)
- [Development Setup](#development-setup)
- [Code Style Guidelines](#code-style-guidelines)
- [Commit Message Guidelines](#commit-message-guidelines)
- [Pull Request Process](#pull-request-process)
- [Testing Guidelines](#testing-guidelines)
- [Issue Reporting](#issue-reporting)

## Code of Conduct

This project adheres to the Contributor Covenant [Code of Conduct](CODE_OF_CONDUCT.md). By participating, you are expected to uphold this code.

## Getting Started

### Types of Contributions

We welcome various types of contributions:

- **Bug Reports**: Help us identify and fix issues
- **Feature Requests**: Suggest new features or improvements
- **Code Contributions**: Submit bug fixes or new features
- **Documentation**: Improve or translate documentation
- **Testing**: Write tests or improve test coverage
- **Code Review**: Review pull requests from other contributors

### Before You Start

1. Check existing [issues](../../issues) and [pull requests](../../pulls) to avoid duplication
2. For major changes, open an issue first to discuss your proposal
3. Read through this contributing guide and the project documentation

## Development Setup

### Prerequisites

- **Go**: >= 1.25
- **Docker**: For running infrastructure services

### Local Environment Setup

See [README.md](./README.md#setup)

### Development Workflow

1. Create a feature branch from `main`
2. Make your changes
3. Write or update tests
4. Run tests locally
5. Commit your changes
6. Push to your fork
7. Open a pull request

## Code Style Guidelines

### Go Code Style

Follow standard Go conventions and best practices:

- **Formatting**: Use `gofmt` or `goimports` before committing
  ```bash
  gofmt -w .
  goimports -w .
  ```

- **Linting**: Run `golangci-lint` to catch common issues
  ```bash
  golangci-lint run
  ```

- **Naming Conventions**:
  - Use `camelCase` for unexported names
  - Use `PascalCase` for exported names
  - Use descriptive names (avoid single-letter variables except in short scopes)
  - Interface names should end with `-er` when possible (e.g., `Reader`, `Writer`)

- **Error Handling**:
  - Always check and handle errors
  - Wrap errors with context using `fmt.Errorf("context: %w", err)`
  - Don't ignore errors with `_` unless absolutely necessary

- **Comments**:
  - Add comments for all exported functions, types, and constants
  - Use complete sentences with proper punctuation
  - Explain *why*, not *what* (code should be self-explanatory)
  - Example:
    ```go
    // CalculateScore computes the relevance score between a user profile
    // and an item based on keyword matching and domain overlap.
    func CalculateScore(profile *Profile, item *Item) float64 {
        // ...
    }
    ```

- **Package Organization**:
  - Keep packages focused and cohesive
  - Avoid circular dependencies
  - Use internal packages for code that shouldn't be imported externally

### Project-Specific Conventions

- **Database Time Fields**: Use `int64` Unix millisecond timestamps, not `time.Time`
- **ID Fields**: Use `BIGINT/i64` internally, return strings in HTTP JSON responses
- **String Storage**: Store comma-separated values for keywords and domains
- **Status Codes**: Use `0=pending, 1=processing, 2=failed, 3=completed`
- **API Response Format**: All responses must include `code` (0=success) and `msg` fields

## Commit Message Guidelines

We follow the [Conventional Commits](https://www.conventionalcommits.org/) specification.

### Format

```
<type>(<scope>): <subject>

<body>

<footer>
```

### Type

- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation changes
- `style`: Code style changes (formatting, missing semicolons, etc.)
- `refactor`: Code refactoring without changing functionality
- `perf`: Performance improvements
- `test`: Adding or updating tests
- `chore`: Maintenance tasks (dependencies, build scripts, etc.)
- `ci`: CI/CD configuration changes

### Scope

The scope should indicate the affected component:
- `api`: API gateway
- `rpc`: RPC services (profile, item, sort, feed, auth, push)
- `pipeline`: Async consumers and LLM processing
- `cache`: Caching layer
- `db`: Database migrations or DAL
- `docs`: Documentation
- `tests`: Test files

### Examples

```
feat(api): add user authentication endpoint

Implement email OTP login flow with session management.
Includes rate limiting and mock OTP whitelist support.

Closes #123
```

```
fix(sort): correct bloom filter deduplication logic

The bloom filter was not properly handling group_id deduplication,
causing duplicate items to appear in feeds.

Fixes #456
```

```
docs(readme): update quick start guide

Add missing environment variable configuration steps
and clarify Docker setup requirements.
```

## Pull Request Process

### Before Submitting

1. **Update your branch**
   ```bash
   git checkout main
   git pull origin main
   git checkout your-feature-branch
   git rebase main
   ```

2. **Run tests**
   ```bash
   go test -v ./...
   ```

3. **Run linters**
   ```bash
   go vet ./...
   golangci-lint run
   ```

4. **Update documentation** if you changed APIs or added features

5. **Add tests** for new functionality

### Submitting a Pull Request

1. Push your branch to your fork
2. Open a pull request against the `main` branch
3. Fill out the PR template completely
4. Link related issues using keywords (e.g., "Closes #123")
5. Request review from maintainers

### PR Review Process

- Maintainers will review your PR within 3-5 business days
- Address review comments by pushing new commits
- Once approved, a maintainer will merge your PR
- PRs require at least one approval before merging

### PR Requirements

- [ ] All tests pass
- [ ] Code follows style guidelines
- [ ] Documentation is updated
- [ ] Commit messages follow conventions
- [ ] No merge conflicts with main branch
- [ ] PR description clearly explains changes

## Testing Guidelines

### Running Tests

```bash
# Run all tests
go test -v ./...

# Run tests for a specific package
go test -v ./rpc/sort/

# Run tests with coverage
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out

# Run integration tests (requires services running)
./scripts/local/start_local.sh
go test -v ./tests/...
```

### Writing Tests

- Place test files next to the code they test (`*_test.go`)
- Use table-driven tests for multiple test cases
- Mock external dependencies (database, Redis, RPC clients)
- Test both success and error cases
- Aim for at least 60% code coverage for new code

Example:
```go
func TestCalculateScore(t *testing.T) {
    tests := []struct {
        name     string
        profile  *Profile
        item     *Item
        expected float64
    }{
        {
            name:     "exact keyword match",
            profile:  &Profile{Keywords: []string{"ai", "ml"}},
            item:     &Item{Keywords: []string{"ai", "ml"}},
            expected: 1.0,
        },
        // More test cases...
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            score := CalculateScore(tt.profile, tt.item)
            if score != tt.expected {
                t.Errorf("got %v, want %v", score, tt.expected)
            }
        })
    }
}
```

## Issue Reporting

### Bug Reports

Use the [bug report template](.github/ISSUE_TEMPLATE/bug_report.md) and include:

- Clear description of the bug
- Steps to reproduce
- Expected vs actual behavior
- Environment details (OS, Go version, etc.)
- Relevant logs or error messages

### Feature Requests

Use the [feature request template](.github/ISSUE_TEMPLATE/feature_request.md) and include:

- Clear description of the feature
- Use case and motivation
- Proposed solution
- Alternative solutions considered

### Security Issues

**Do not** open public issues for security vulnerabilities. Instead, email security@eigenflux.one with details.

## Additional Resources

- [Project Documentation](docs/)
- [Architecture Overview](docs/architecture_overview.md)
- [API Documentation](http://localhost:8080/swagger/index.html)
- [Development Guidelines](CLAUDE.md)

## Questions?

If you have questions about contributing, feel free to:

- Open a [discussion](../../discussions)
- Ask in issues
- Contact the maintainers

Thank you for contributing to EigenFlux! 🎉
