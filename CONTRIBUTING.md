# Contributing to Swig

We love your input! We want to make contributing to Swig as easy and transparent as possible, whether it's:

- Reporting a bug
- Discussing the current state of the code
- Submitting a fix
- Proposing new features
- Becoming a maintainer

## We Develop with Github
We use GitHub to host code, to track issues and feature requests, as well as accept pull requests.

## Pull Requests
1. Fork the repo and create your branch from `main`.
2. If you've added code that should be tested, add tests.
3. If you've changed APIs, update the documentation.
4. Ensure the test suite passes.
5. Make sure your code lints.
6. Issue that pull request!

## Any contributions you make will be under the MIT Software License
In short, when you submit code changes, your submissions are understood to be under the same [MIT License](LICENSE) that covers the project. Feel free to contact the maintainers if that's a concern.

## Report bugs using Github's [issue tracker](https://github.com/swig/swig-go/issues)
We use GitHub issues to track public bugs. Report a bug by [opening a new issue](https://github.com/swig/swig-go/issues/new).

## Write bug reports with detail, background, and sample code

**Great Bug Reports** tend to have:

- A quick summary and/or background
- Steps to reproduce
  - Be specific!
  - Give sample code if you can.
- What you expected would happen
- What actually happens
- Notes (possibly including why you think this might be happening, or stuff you tried that didn't work)

## Development Process

1. Clone the repository:
   ```bash
   git clone https://github.com/swig.git
   ```

2. Install dependencies:
   ```bash
   cd swig-go
   go mod download
   ```

3. Run tests:
   ```bash
   go test -v ./...
   ```

4. Run linter:
   ```bash
   golangci-lint run
   ```

## Testing
We use Go's standard testing package. Please write tests for new code you create.

## Coding Style
- Follow standard Go formatting guidelines
- Run `go fmt` before committing
- Use meaningful variable names
- Comment complex logic
- Keep functions focused and small

## License
By contributing, you agree that your contributions will be licensed under its MIT License. 