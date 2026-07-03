// Package db embeds the platform PostgreSQL schema/migrations so they ship
// inside the operator binary, yet stay reviewable and version-controlled as
// plain .sql files under db/migrations/.
//
// Using //go:embed (rather than reading files at runtime) means there is no
// fragile container path to mount and no Dockerfile COPY of the SQL into the
// runtime image — the schema is compiled into the binary at build time.
package db

import (
	"embed"
	"io/fs"
	"sort"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migration is one ordered .sql file from db/migrations/.
type Migration struct {
	Name string // filename, e.g. "0001_initial_schema.sql"
	SQL  string // file contents
}

// LoadMigrations returns every embedded migration sorted by filename.
// Filenames are zero-padded and numeric-prefixed, so lexicographic order is the
// apply order (0001 before 0002, ...).
func LoadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	out := make([]Migration, 0, len(names))
	for _, n := range names {
		b, err := migrationsFS.ReadFile("migrations/" + n)
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{Name: n, SQL: string(b)})
	}
	return out, nil
}
