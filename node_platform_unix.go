//go:build !windows

package main

// Node files are protected by their 0600 mode on Unix; passwords are stored
// as is.
func protectNodePassword(password string) (string, error) {
	return password, nil
}

func unprotectNodePassword(password string) (string, error) {
	return password, nil
}
