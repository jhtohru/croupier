package postgres

import "embed"

// MigrationsFS embeds every migration file into the binary, so cmd/croupier
// can apply them on startup (via golang-migrate's iofs source) without
// needing the migrations directory mounted or copied separately into a
// container — the README's manual `docker run migrate/migrate` commands
// still work unchanged for anyone who prefers running them by hand.
//
//go:embed migrations/*.sql
var MigrationsFS embed.FS
