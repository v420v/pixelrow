package tools

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Tunnel forwards a port of this machine to a host that only an SSH server reaches, through a running ssh.
type Tunnel struct {
	// Port is the port on 127.0.0.1 that leads to the remote host.
	Port int

	cmd *exec.Cmd
	// exited is closed once ssh has ended, after which err says how.
	exited chan struct{}
	err    error
	stderr *tailBuffer
}

// tunnelStartLimit is how long ssh gets to connect and open the forward.
const tunnelStartLimit = 30 * time.Second

// Server is the SSH server a tunnel goes through.
type Server struct {
	// Destination is what one would type after "ssh" in a terminal: a host, or user@host.
	Destination string
	// Port is 0 for the port ssh would use by itself.
	Port int
	// Key is the path of the private key to sign in with, or "" for the keys ssh would try by itself.
	Key string
	// Passphrase unlocks the key, and Password signs in to the server. Either can be empty.
	Passphrase string
	Password   string
	// Askpass is the path of a program that calls Askpass, which hands ssh the two secrets above. They need it.
	Askpass string
}

// OpenTunnel starts the ssh at program towards server, and returns once connections to the tunnel's port reach host:port as seen from the server.
//
// ssh runs without a terminal, so it cannot ask the user for anything: a passphrase comes from server, an agent or the keychain,
// and the server's host key must already be known.
func OpenTunnel(ctx context.Context, program string, server Server, host string, port int) (*Tunnel, error) {
	// A destination starting with a dash would be read by ssh as an option.
	if server.Destination == "" || strings.HasPrefix(server.Destination, "-") {
		return nil, fmt.Errorf("invalid ssh destination %q", server.Destination)
	}
	var extra []string
	if server.Port != 0 {
		extra = append(extra, "-p", strconv.Itoa(server.Port))
	}
	if server.Key != "" {
		key := server.Key
		if rest, ok := strings.CutPrefix(key, "~/"); ok {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			key = filepath.Join(home, rest)
		}
		// ssh would pass over a key it cannot read and try others, and then fail without naming the cause.
		if _, err := os.Stat(key); err != nil {
			return nil, fmt.Errorf("the ssh key is not at %s", key)
		}
		// Only this key is offered: a server closes the connection after a few keys it does not take.
		extra = append(extra, "-i", key, "-o", "IdentitiesOnly=yes")
	}
	local, err := freePort()
	if err != nil {
		return nil, err
	}
	forward := fmt.Sprintf("127.0.0.1:%d:%s", local, net.JoinHostPort(host, strconv.Itoa(port)))
	t := &Tunnel{Port: local, exited: make(chan struct{}), stderr: &tailBuffer{max: 4 << 10}}
	t.cmd = exec.Command(program,
		"-N", "-L", forward,
		// An unknown host key fails here, whether or not ssh has a way to ask about it.
		"-o", "StrictHostKeyChecking=yes",
		// Without this ssh stays up with no forward when the local port is taken, and nothing would tell.
		"-o", "ExitOnForwardFailure=yes",
		// A tunnel whose network went away ends within a minute and a half rather than hang its connections.
		"-o", "ServerAliveInterval=30", "-o", "ServerAliveCountMax=3",
		"-o", "ConnectTimeout=15")
	if server.Passphrase == "" && server.Password == "" {
		extra = append(extra, "-o", "BatchMode=yes")
	} else {
		if server.Askpass == "" {
			return nil, errors.New("no program to hand ssh the passphrase")
		}
		// BatchMode would keep ssh from asking at all. A wrong secret fails at once rather than be asked for three times.
		extra = append(extra, "-o", "NumberOfPasswordPrompts=1")
		t.cmd.Env = append(os.Environ(),
			"SSH_ASKPASS="+server.Askpass,
			// Without a terminal ssh only uses the askpass when told to.
			"SSH_ASKPASS_REQUIRE=force",
			askpassEnv+"=1", passphraseEnv+"="+server.Passphrase, passwordEnv+"="+server.Password)
	}
	t.cmd.Args = append(t.cmd.Args, extra...)
	t.cmd.Args = append(t.cmd.Args, server.Destination)
	t.cmd.Stderr = t.stderr
	if err := t.cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		t.err = t.cmd.Wait()
		close(t.exited)
	}()

	deadline := time.After(tunnelStartLimit)
	for {
		if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(local)), time.Second); err == nil {
			conn.Close()
			return t, nil
		}
		select {
		case <-t.exited:
			return nil, t.failure()
		case <-ctx.Done():
			t.Close()
			return nil, ctx.Err()
		case <-deadline:
			t.Close()
			return nil, errors.New("ssh did not open the tunnel in time")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (t *Tunnel) Alive() bool {
	select {
	case <-t.exited:
		return false
	default:
		return true
	}
}

func (t *Tunnel) Close() {
	t.cmd.Process.Kill()
	<-t.exited
}

func (t *Tunnel) failure() error {
	if msg := strings.TrimSpace(t.stderr.String()); msg != "" {
		return errors.New(msg)
	}
	return fmt.Errorf("ssh ended: %v", t.err)
}

// freePort returns a port nothing listens on. Another program can take it before ssh does, which ExitOnForwardFailure then reports.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
