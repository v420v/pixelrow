package tools

import (
	"slices"
	"testing"
)

func TestPgConnect(t *testing.T) {
	arg, env := pgConnect(Target{
		Host: "127.0.0.1", Port: 5433, User: "app", Password: "p", Database: "it's", ServerName: "db.internal",
		Options: map[string]string{"sslmode": "verify-full", "search_path": "a, b", "sslpassword": "secret", "options": "-c x=1"},
	})
	want := `--dbname=host='db.internal' hostaddr='127.0.0.1' port='5433' user='app' dbname='it\'s' sslmode='verify-full' options='-c x=1 -c search_path=a,\\ b'`
	if arg != want {
		t.Errorf("got  %s\nwant %s", arg, want)
	}
	if !slices.Equal(env, []string{"PGPASSWORD=p"}) {
		t.Errorf("env = %v", env)
	}
}

func TestMysqlTLS(t *testing.T) {
	for _, c := range []struct {
		target Target
		want   []string
	}{
		{Target{}, nil},
		{Target{Options: map[string]string{"tls": "true", "charset": "utf8mb4"}}, []string{"--ssl-mode=VERIFY_IDENTITY"}},
		{Target{Options: map[string]string{"tls": "skip-verify"}}, []string{"--ssl-mode=REQUIRED"}},
		{Target{ServerName: "db", Options: map[string]string{"ssl-ca": "/ca.pem"}}, []string{"--ssl-ca=/ca.pem", "--ssl-mode=VERIFY_CA"}},
	} {
		if got := mysqlTLS(c.target); !slices.Equal(got, c.want) {
			t.Errorf("%v: got %v, want %v", c.target.Options, got, c.want)
		}
	}
}
