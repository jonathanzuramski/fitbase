# Contributing to fitbase

Thank you for taking the time to contribute. fitbase is a small project and every improvement matters.

## Prerequisites

- [Go 1.24+](https://go.dev/dl/)
- No CGO, no Docker, no npm — the entire stack is a single Go binary

## Getting started

```bash
git clone https://github.com/fitbase/fitbase
cd fitbase
go build -o fitbase ./cmd/fitbase
FITBASE_DEV=true ./fitbase
```

Open `http://localhost:8080`. `FITBASE_DEV=true` serves templates and static files directly from disk, so CSS/HTML changes take effect on browser refresh without restarting the server.

## Running tests

```bash
go test ./...
```

All tests use real SQLite (no mocks) and real FIT file decoding. There are no external dependencies needed.

## Project layout

```
cmd/fitbase/main.go          Entry point, HTTP handlers for UI pages
internal/
  api/                       JSON REST API handlers and router
  config/                    Env-var config loading
  db/                        SQLite layer (db.go + schema.sql)
  fit/                       FIT file parser
  models/                    Shared domain types
  sync/                      File watcher, importer, cloud sync stubs
  gdrive/                    Google Drive integration
web/
  templates/                 Go html/template server-side pages
  static/css/style.css       Dark theme CSS
  static/js/charts.js        uPlot chart wrappers
openapi.yaml                 REST API spec — keep this in sync with handlers
```

## Adding a feature

1. **API changes first**: update `openapi.yaml` before writing handler code.
2. **DB changes**: add a new migration file rather than editing existing ones. Schema is in `internal/db/schema.sql`; migrations run as `ALTER TABLE` statements in `db.Open()`.
3. **FIT parsing**: documented in `internal/fitparser/parser.go`.
4. **Templates**: all pages extend `web/templates/base.html`. Use the existing CSS variables in `style.css` rather than inline colors.
5. **Tests**: add a test for any new parsing logic, DB query, or API handler. The existing test files show the patterns.

## Code style

- Standard `gofmt` / `goimports` formatting — run before committing
- `log/slog` for all server-side logging — no `fmt.Print` in production paths
- Plain `database/sql` — no ORM
- No external test frameworks — standard `testing` package only
- Errors are wrapped with context: `fmt.Errorf("doing thing: %w", err)`

## Submitting a pull request

1. Fork the repo and create a branch from `main`
2. Make your changes with tests where applicable
3. Run `go test ./...` and `go vet ./...` — both must pass
4. Keep PRs focused; one concern per PR
5. Write a clear description of _what_ changed and _why_

## Reporting a bug

Use the GitHub issue tracker. Please include:

- Your device type and how you're getting FIT files onto fitbase
- The sport type (cycling, running, etc.)
- What you expected vs. what happened
- Attaching a sanitized `.fit` file (with GPS stripped if preferred) is extremely helpful

## License

By contributing you agree that your contributions will be licensed under the MIT License.
