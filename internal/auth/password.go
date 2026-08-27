package auth

import (
	"fmt"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// bcrypt is intentionally used instead of a home-grown password KDF. It is
// pure Go, has a well-reviewed implementation, and remains compatible with a
// static Linux build.
const bcryptCost = 12

func HashPassword(password string) (string, error) {
	if utf8.RuneCountInString(password) < 12 {
		return "", fmt.Errorf("password must be at least 12 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	return string(hash), err
}

func CheckPassword(password, encoded string) bool {
	return bcrypt.CompareHashAndPassword([]byte(encoded), []byte(password)) == nil
}
