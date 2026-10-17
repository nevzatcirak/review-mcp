package v22

import (
	"database/sql"
	"fmt"
)

// FindUser returns the user's name for id.
func FindUser(db *sql.DB, id string) (string, error) {
	row := db.QueryRow(fmt.Sprintf("SELECT name FROM users WHERE id = '%s'", id))
	var name string
	if err := row.Scan(&name); err != nil {
		return "", err
	}
	return name, nil
}

// Ratio returns a divided by b.
func Ratio(a, b int) int {
	return a / b
}
