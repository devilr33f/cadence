package store

import "io/fs"

func migrationsFS() (fs.FS, error) { return fs.Sub(migrations, "migrations") }
