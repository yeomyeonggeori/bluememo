package migrations

import (
	"embed"
	"io/fs"
	"sort"
)

//go:embed *.sql
var Files embed.FS

type Migration struct {
	Name string
	SQL  string
}

func List() ([]Migration, error) {
	entries, errorValue := fs.ReadDir(Files, ".")
	if errorValue != nil {
		return nil, errorValue
	}
	migrations := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		document, errorValue := fs.ReadFile(Files, entry.Name())
		if errorValue != nil {
			return nil, errorValue
		}
		migrations = append(migrations, Migration{Name: entry.Name(), SQL: string(document)})
	}
	sort.Slice(migrations, func(left int, right int) bool { return migrations[left].Name < migrations[right].Name })
	return migrations, nil
}
