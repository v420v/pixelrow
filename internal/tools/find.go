// Package tools runs the command-line programs of the databases: mysqldump and pg_dump to dump, mysql and psql to run SQL files.
package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// A desktop app does not get the PATH of a shell, which is why PATH alone is not enough.
func Find(name, configured string) (string, error) {
	if configured != "" {
		if _, err := os.Stat(configured); err != nil {
			return "", fmt.Errorf("%s is not at %s", name, configured)
		}
		return configured, nil
	}
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	home, _ := os.UserHomeDir()
	for _, dir := range []string{
		"/opt/homebrew/bin", "/usr/local/bin",
		"/opt/homebrew/opt/libpq/bin", "/usr/local/opt/libpq/bin",
		"/opt/homebrew/opt/mysql-client/bin", "/usr/local/opt/mysql-client/bin",
		"/Applications/Postgres.app/Contents/Versions/latest/bin",
		"/usr/local/mysql/bin",
		"/etc/profiles/per-user/" + os.Getenv("USER") + "/bin", "/run/current-system/sw/bin",
		filepath.Join(home, ".nix-profile", "bin"),
	} {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("%s was not found", name)
}
