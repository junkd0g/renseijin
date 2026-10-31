# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Fixed
- Recursive input schemas now use local JSON Schema references instead of
  overflowing the stack during registration.
- JSON number precision is preserved through argument decoding and HTTP encoding.
- Response limits bound reads to the limit plus one byte; truncated responses
  no longer require buffering the entire upstream body.
- Tool arguments are validated before dispatch, including required bodies and
  nested fields. Invalid schemas fail registration before any tools are added.
- Parameter encoding honors supported OpenAPI styles and `explode`; unsupported
  encodings fail explicitly. Form body arrays use repeated fields.
- Duplicate generated tool names within a registration fail before any tools
  are added. Cross-location parameter names and the reserved body name receive
  unambiguous aliases.
- Operation and path server overrides take precedence over root servers.
- Schema generation and HTTP encoding share a deterministic request media type.
- Multipart binary fields advertise the required filename/base64 envelope,
  including arrays of files.

### Compatibility
- Invalid tool arguments previously forwarded to the API now return tool errors.
- Colliding parameter names use generated aliases; inspect the tool schema for
  their exact names. Non-colliding names are unchanged.
- Truncation markers report bytes shown without an unknown total; negative
  response limits are rejected.

### Added
- GitHub Actions CI (`.github/workflows/ci.yml`) running `gofmt`, `go vet`,
  `go test -race`, `golangci-lint` (v2.12.2 via `golangci-lint-action@v8`,
  required for Go 1.25 targets), and `govulncheck` on Go 1.25.
- `.gitignore` covering Go build artifacts, coverage profiles, and macOS metadata.
- `LICENSE` file (MIT).
- `CHANGELOG.md` following Keep a Changelog format.
- README: report-card / license / GoDoc badges, License section, and Author section.

### Changed
- Licence: previously "Not chosen yet" — now MIT, matching the other `junkd0g/*` repos.
