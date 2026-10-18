// Package userclient reads users from the profile service.
package userclient

import (
	"encoding/json"
	"fmt"
)

// User is one user as the profile service returns it.
type User struct {
	ID       string `json:"id"`
	UserName string `json:"userName"`
}

// Decode reads one user from the profile service's JSON.
func Decode(data []byte) (User, error) {
	var u User
	if err := json.Unmarshal(data, &u); err != nil {
		return User{}, fmt.Errorf("decode user: %w", err)
	}
	return u, nil
}

// Label is the name shown for a user in lists.
func Label(u User) string {
	return u.UserName + " (" + u.ID + ")"
}
