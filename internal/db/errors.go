package db

import (
	"errors"

	"github.com/mattn/go-sqlite3"
)

// IsUniqueViolation reports whether err is a SQLite primary-key or UNIQUE
// constraint violation.
func IsUniqueViolation(err error) bool {
	var se sqlite3.Error
	if errors.As(err, &se) {
		return se.ExtendedCode == sqlite3.ErrConstraintPrimaryKey ||
			se.ExtendedCode == sqlite3.ErrConstraintUnique
	}
	return false
}
