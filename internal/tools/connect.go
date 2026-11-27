package tools

import (
	"fmt"
	"maps"
	"net"
	"slices"
	"strings"
)

// Target is the database a tool connects to.
type Target struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
	// Options are the options of the connection, by the names the app's own drivers give them.
	Options map[string]string
	// ServerName is the host the server's certificate is checked against when Host is the local end of a tunnel, or "".
	ServerName string
}

// libpqParams are the options psql and pg_dump take as connection parameters.
// The others are settings of the session, which is what the app's own driver makes of them.
var libpqParams = []string{
	"application_name", "channel_binding", "client_encoding", "connect_timeout", "krbsrvname",
	"max_protocol_version", "min_protocol_version", "passfile", "require_auth", "service",
	"sslcert", "sslcertmode", "sslkey", "sslmode", "sslnegotiation", "sslrootcert", "sslsni", "target_session_attrs",
}

// pgConnect returns the argument and the environment that make psql and pg_dump connect to t.
func pgConnect(t Target) (arg string, env []string) {
	var b strings.Builder
	param := func(key, value string) {
		value = strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value)
		fmt.Fprintf(&b, "%s='%s' ", key, value)
	}
	if t.ServerName != "" && net.ParseIP(t.Host) != nil {
		// The certificate is checked against host while the connection goes to hostaddr.
		param("host", t.ServerName)
		param("hostaddr", t.Host)
	} else {
		param("host", t.Host)
	}
	if t.Port != 0 {
		param("port", fmt.Sprint(t.Port))
	}
	param("user", t.User)
	if t.Database != "" {
		param("dbname", t.Database)
	}
	env = []string{"PGPASSWORD=" + t.Password}
	settings := t.Options["options"]
	for _, key := range slices.Sorted(maps.Keys(t.Options)) {
		value := t.Options[key]
		switch {
		case slices.Contains(libpqParams, key):
			param(key, value)
		case key == "servicefile":
			env = append(env, "PGSERVICEFILE="+value)
		// sslpassword would show in the arguments, which other users of the machine can list, and krbspn is the driver's alone.
		case key == "options" || key == "sslpassword" || key == "krbspn":
		default:
			settings += " -c " + key + "=" + strings.NewReplacer(`\`, `\\`, ` `, `\ `).Replace(value)
		}
	}
	if settings != "" {
		param("options", strings.TrimSpace(settings))
	}
	return "--dbname=" + strings.TrimSpace(b.String()), env
}

// mysqlTLS returns the arguments that make mysql and mysqldump encrypt as the options of t ask the driver to.
// The other options are the driver's own, and mean nothing to these programs.
func mysqlTLS(t Target) []string {
	var args []string
	for _, key := range []string{"ssl-ca", "ssl-cert", "ssl-key"} {
		if t.Options[key] != "" {
			args = append(args, "--"+key+"="+t.Options[key])
		}
	}
	mode := strings.ToLower(t.Options["tls"])
	if mode == "" && len(args) > 0 {
		mode = "true"
	}
	switch mode {
	case "true", "1":
		// ponytail: these programs have no way to check a name other than the host they connect to, so through a tunnel only the chain is checked.
		if t.ServerName != "" {
			return append(args, "--ssl-mode=VERIFY_CA")
		}
		return append(args, "--ssl-mode=VERIFY_IDENTITY")
	case "skip-verify":
		return append(args, "--ssl-mode=REQUIRED")
	case "preferred":
		return append(args, "--ssl-mode=PREFERRED")
	case "false", "0":
		return append(args, "--ssl-mode=DISABLED")
	}
	return args
}
