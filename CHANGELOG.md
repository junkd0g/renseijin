# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
- GitHub Actions CI (`.github/workflows/ci.yml`) running `gofmt`, `go vet`,
  `go test -race`, `golangci-lint`, and `govulncheck` on Go 1.25.
- `.gitignore` covering Go build artifacts, coverage profiles, and macOS metadata.
- `LICENSE` file (MIT).
- `CHANGELOG.md` following Keep a Changelog format.
- README: report-card / license / GoDoc badges, License section, and Author section.

### Changed
- Licence: previously "Not chosen yet" — now MIT, matching the other `junkd0g/*` repos.
