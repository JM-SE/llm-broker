// Package migrations embeds the SQL migration files into the binary so the
// broker can migrate without external files, while still keeping migrations
// human-readable and reviewable in the repository.
package migrations

import (
	"embed"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
)

//go:embed *.sql
var FS embed.FS

// LatestVersion returns the highest numeric migration version found in the
// embedded filesystem. Files must use the goose numeric prefix convention,
// for example "00001_initial_schema.sql" is version 1.
func LatestVersion() (int64, error) {
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		return 0, fmt.Errorf("read embedded migrations: %w", err)
	}
	var latest int64
	found := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			return 0, fmt.Errorf("migration %q lacks the numeric version prefix", e.Name())
		}
		v, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("migration %q has an invalid version prefix: %w", e.Name(), err)
		}
		if v > latest {
			latest = v
		}
		found = true
	}
	if !found {
		return 0, fmt.Errorf("no embedded SQL migrations found")
	}
	return latest, nil
}
