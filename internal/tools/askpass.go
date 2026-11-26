package tools

import (
	"fmt"
	"os"
	"strings"
)

// ssh has no option that takes a passphrase or a password. It asks a program instead, the one SSH_ASKPASS names,
// which the app itself plays: started again with these variables set, it prints the secret and ends.
const (
	askpassEnv    = "PIXELROW_SSH_ASKPASS"
	passphraseEnv = "PIXELROW_SSH_PASSPHRASE"
	passwordEnv   = "PIXELROW_SSH_PASSWORD"
)

// Askpass answers ssh when the program was started as its askpass, and reports whether it was.
// A program that calls it first thing in main, and returns when it reports true, can be the Askpass of a Server.
func Askpass() bool {
	if os.Getenv(askpassEnv) == "" {
		return false
	}
	prompt := strings.ToLower(strings.Join(os.Args[1:], " "))
	var secret string
	switch {
	case strings.Contains(prompt, "passphrase"):
		secret = os.Getenv(passphraseEnv)
	case strings.Contains(prompt, "password"):
		secret = os.Getenv(passwordEnv)
	}
	// Anything else ssh asks, such as whether to trust a host key, is declined rather than answered blindly.
	if secret == "" {
		os.Exit(1)
	}
	fmt.Println(secret)
	return true
}
