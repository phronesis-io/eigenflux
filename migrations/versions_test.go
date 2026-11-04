package migrations_test

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMigrationVersionsAreUnique(t *testing.T) {
	files, err := filepath.Glob("*.sql")
	if err != nil {
		t.Fatal(err)
	}
	versions := make(map[int64]string)
	for _, file := range files {
		prefix, _, ok := strings.Cut(file, "_")
		if !ok {
			t.Fatalf("migration has no numeric prefix: %s", file)
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil || version <= 0 {
			t.Fatalf("invalid migration version: %s", file)
		}
		if prior, ok := versions[version]; ok {
			t.Fatalf("duplicate migration version %d: %s and %s", version, prior, file)
		}
		versions[version] = file
	}
	if len(versions) == 0 {
		t.Fatal("no migrations found")
	}
}
