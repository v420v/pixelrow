package app

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/egoist/mygo/ui"

	"github.com/v420v/pixelrow/internal/config"
	"github.com/v420v/pixelrow/internal/driver"
	"github.com/v420v/pixelrow/internal/tools"
)

// connections is the tree of the config file's connections, each holding its schemas and tables once opened.
type connections struct {
	path     string
	drivers  map[string]driver.Driver
	update   func(fn func())
	openFile func(path string) error
	// askpass is the path of the program that hands ssh a passphrase: the app itself.
	askpass string
	onQuery func(conn string)
	onTable func(conn, schema, table string)
	// onDisconnect is called after a connection is disconnected, or changed or removed in the config file.
	onDisconnect func(conn string)
	// onDump is called to dump table of schema, or the whole schema when table is "".
	onDump    func(conn, schema, table string)
	onDiagram func(conn, schema string)
	// onRefresh is called when the user asks to read a connection's tables again.
	onRefresh func(conn string)
	// onRunFile is called to run a SQL file on a connection, in database when it is not "".
	onRunFile func(conn, database string)
	footer    func(c *ui.Context)

	loaded  bool
	nodes   []*connNode
	loadErr error
	// tools maps the programs the config file places to their paths.
	tools map[string]string
	// selected is the name of the connection new queries open on.
	selected      string
	selectedTable string
	fileErr       error

	// mu guards confs, tools and tunnels, which connections being opened read off the UI thread.
	mu      sync.RWMutex
	confs   map[string]config.Connection
	tunnels map[string]*tunnel
}

type connNode struct {
	conf config.Connection
	open bool
	// browser is nil while the connection is not opened.
	browser *browser
}

func (s *connections) view(c *ui.Context) {
	t := c.Theme()
	if !s.loaded {
		s.loaded = true
		s.reload()
	}

	ui.Column(c).Fill().Background(t.Surface).Children(func() {
		ui.Row(c).FillWidth().Height(barHeight).Shrink(0).Padding(0, t.Space(2), 0, t.Space(3)).Gap(t.Space(0.5)).AlignItems(ui.Center).Children(func() {
			heading(c, "CONNECTIONS")
			ui.Spacer(c)
			iconButton(c, iconFile, "Open Config File").Tooltip("Open " + s.path).OnClick(s.openConfig)
			iconButton(c, iconReload, "Reload").Tooltip("Read the config file again").OnClick(s.reload)
		})
		hairline(c)
		ui.Scroll(c).Grow(1).FillWidth().Padding(t.Space(1), 0).Children(func() {
			switch {
			case s.loadErr != nil:
				ui.Text(c, "The config file could not be read: "+s.loadErr.Error()).TextColor(t.Danger).Padding(t.Space(2)).Selectable()
			case len(s.nodes) == 0:
				ui.Text(c, "No connections yet. Add a [connections.<name>] table to the config file, then reload.").TextColor(t.TextMuted).Padding(t.Space(2))
			}
			ui.Tree(c, func() {
				for _, n := range s.nodes {
					s.connItem(c, n)
				}
			})
		})
		s.footer(c)
		if s.fileErr != nil {
			ui.Divider(c)
			ui.Text(c, "Could not open the config file: "+s.fileErr.Error()).TextColor(t.Danger).FontSize(11).Padding(t.Space(2))
		}
	})
}

func (s *connections) connItem(c *ui.Context, n *connNode) {
	name := n.conf.Name
	// Opening the item connects, and closing one that failed lets the next opening try again.
	if n.open && n.browser == nil {
		s.connect(n)
	}
	if !n.open && n.browser != nil && n.browser.err != nil {
		n.browser.close()
		n.browser = nil
	}

	chosen := s.selected == name && s.selectedTable == ""
	// The row has no label of the tree's own: the logo goes before the name, where a label would come first.
	item := ui.TreeItem(c, "", &n.open, func() {
		if n.browser != nil {
			n.browser.items(c, &s.selectedTable, name, treeActions{
				openTable: func(schema, table string) {
					s.selected = name
					s.onTable(name, schema, table)
				},
				dump:    func(schema, table string) { s.onDump(name, schema, table) },
				diagram: func(schema string) { s.onDiagram(name, schema) },
				runFile: s.runFileIn(n),
			})
		}
	}).Label(name).Tooltip(n.conf.Kind + " · " + address(n.conf) + readOnlyNote(n.conf))
	chosenLook(c, item, chosen)
	t := c.Theme()
	dot, state := t.Border, "Not connected"
	switch {
	case n.connected():
		dot, state = t.Success, "Connected"
	case n.browser != nil && n.browser.err != nil:
		dot, state = t.Danger, "Could not connect"
	case n.browser != nil:
		dot, state = t.Warning, "Connecting"
	}
	item.Children(func() {
		logo(c, n.conf.Kind)
		ui.Column(c).Width(t.Space(1))
		ui.Text(c, name).SingleLine()
		if n.conf.Color != "" {
			ui.Column(c).Width(t.Space(1))
			colorDot(c, ui.Hex(n.conf.Color))
		}
		ui.Spacer(c)
		ui.Column(c).Width(8).Height(8).Radius(4).Shrink(0).Background(dot).Label(name + ": " + state)
	})

	var disconnect, refresh bool
	item.ContextMenu(func(m *ui.Menu) {
		if m.Item("New Query").Chosen() {
			s.selected, s.selectedTable = name, ""
			s.onQuery(name)
		}
		if m.Item("Run SQL File…").Disabled(n.conf.ReadOnly).Chosen() {
			s.onRunFile(name, "")
		}
		m.Separator()
		if m.Item("Refresh").Disabled(n.browser == nil).Chosen() {
			refresh = true
		}
		if m.Item("Disconnect").Disabled(n.browser == nil).Chosen() {
			disconnect = true
		}
	})
	switch {
	case disconnect:
		s.disconnect(n)
	case refresh:
		n.browser.load()
		s.onRefresh(name)
	case item.Clicked():
		// A click on the name chooses the connection and opens it, not only its arrow.
		s.selected, s.selectedTable = name, ""
		n.open = !n.open
	}
}

// runFileIn returns what runs a SQL file in a schema of n, or nil where a file cannot be aimed at one.
func (s *connections) runFileIn(n *connNode) func(schema string) {
	// The client is another program, which the read-only setting of the app's own sessions does not hold back.
	if client := tools.Clients[n.conf.Kind]; client == nil || !client.PerSchema || n.conf.ReadOnly {
		return nil
	}
	return func(schema string) { s.onRunFile(n.conf.Name, schema) }
}

func (s *connections) refresh(conn string) {
	for _, n := range s.nodes {
		if n.conf.Name == conn && n.browser != nil {
			n.browser.load()
		}
	}
}

// connected reports whether the connection has been opened and answered.
func (n *connNode) connected() bool { return n.browser != nil && n.browser.loaded }

func (s *connections) connect(n *connNode) {
	name := n.conf.Name
	n.browser = &browser{
		open:   func(ctx context.Context) (driver.Conn, error) { return s.dial(ctx, name) },
		update: s.update,
	}
	n.browser.load()
}

func (s *connections) disconnect(n *connNode) {
	if n.browser != nil {
		n.browser.close()
		n.browser = nil
	}
	n.open = false
	s.onDisconnect(n.conf.Name)
	s.closeTunnel(n.conf.Name)
}

// reload reads the config file again. Connections it changed or removed are disconnected.
func (s *connections) reload() {
	// A first run leaves an example to start from.
	if err := config.Create(s.path); err != nil {
		s.loadErr = err
		return
	}
	file, err := config.Load(s.path)
	s.loadErr = err
	list := file.Connections
	if err != nil {
		// The connections already open stay as they are until the file reads again.
		return
	}

	old := map[string]*connNode{}
	for _, n := range s.nodes {
		old[n.conf.Name] = n
	}
	confs := map[string]config.Connection{}
	nodes := make([]*connNode, len(list))
	for i, conf := range list {
		confs[conf.Name] = conf
		n := old[conf.Name]
		if n != nil && n.conf == conf {
			delete(old, conf.Name)
		} else {
			n = &connNode{conf: conf}
		}
		nodes[i] = n
	}
	s.mu.Lock()
	s.confs, s.tools = confs, file.Tools
	s.mu.Unlock()
	s.nodes = nodes
	for _, n := range old {
		if n.browser != nil {
			n.browser.close()
		}
		s.onDisconnect(n.conf.Name)
		s.closeTunnel(n.conf.Name)
	}
	if _, ok := confs[s.selected]; !ok {
		s.selected, s.selectedTable = "", ""
	}
}

func (s *connections) openConfig() {
	s.fileErr = s.openFile(s.path)
}

// conf returns the connection named name as the config file last had it. It is safe from any goroutine.
func (s *connections) conf(name string) (config.Connection, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	conf, ok := s.confs[name]
	return conf, ok
}

// dial opens a session on the connection named name. It runs off the UI thread.
func (s *connections) dial(ctx context.Context, name string) (driver.Conn, error) {
	conf, ok := s.conf(name)
	if !ok {
		return nil, fmt.Errorf("connection %q is no longer in the config file", name)
	}
	drv := s.drivers[conf.Kind]
	if drv == nil {
		return nil, fmt.Errorf("unknown kind %q of connection %q", conf.Kind, conf.Name)
	}
	host, port, err := s.endpoint(ctx, conf)
	if err != nil {
		return nil, err
	}
	return drv.Open(ctx, driver.Config{
		Host:     host,
		Port:     port,
		User:     conf.User,
		Password: conf.Password,
		Database: conf.Database,
		ReadOnly: conf.ReadOnly,
	})
}

// tunnel is the SSH tunnel of one connection, shared by all its sessions.
type tunnel struct {
	// mu is held while the tunnel opens, so that sessions starting together wait for one ssh rather than start several.
	mu sync.Mutex
	// conf is what the tunnel was opened for: a change in the config file needs a new one.
	conf config.Connection
	open *tools.Tunnel
}

// endpoint returns where to connect to reach the database of conf: the database itself, or the local end of its SSH tunnel, opened if needed.
// It runs off the UI thread.
func (s *connections) endpoint(ctx context.Context, conf config.Connection) (host string, port int, err error) {
	if conf.SSH.Host == "" {
		return conf.Host, conf.Port, nil
	}
	s.mu.Lock()
	if s.tunnels == nil {
		s.tunnels = map[string]*tunnel{}
	}
	t := s.tunnels[conf.Name]
	if t == nil {
		t = &tunnel{}
		s.tunnels[conf.Name] = t
	}
	program := s.tools["ssh"]
	s.mu.Unlock()

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.open != nil && (!t.open.Alive() || t.conf != conf) {
		t.open.Close()
		t.open = nil
	}
	if t.open == nil {
		ssh, err := tools.Find("ssh", program)
		if err != nil {
			return "", 0, err
		}
		remote := conf.Port
		if remote == 0 {
			remote = s.drivers[conf.Kind].DefaultPort()
		}
		server := tools.Server{
			Destination: conf.SSH.Destination(),
			Port:        conf.SSH.Port,
			Key:         conf.SSH.Key,
			Passphrase:  conf.SSH.Passphrase,
			Password:    conf.SSH.Password,
			Askpass:     s.askpass,
		}
		opened, err := tools.OpenTunnel(ctx, ssh, server, conf.Host, remote)
		if err != nil {
			return "", 0, fmt.Errorf("SSH tunnel through %s: %w", conf.SSH.Destination(), err)
		}
		t.open, t.conf = opened, conf
	}
	return "127.0.0.1", t.open.Port, nil
}

// Sessions through the tunnel are cut.
func (s *connections) closeTunnel(name string) {
	s.mu.Lock()
	t := s.tunnels[name]
	delete(s.tunnels, name)
	s.mu.Unlock()
	if t == nil {
		return
	}
	// The tunnel may be opening, which is waited for off the UI thread.
	go func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.open != nil {
			t.open.Close()
			t.open = nil
		}
	}()
}

// Close ends every SSH tunnel, and waits for them, as the app quits: an ssh left running would outlive it.
func (s *connections) Close() {
	s.mu.Lock()
	tunnels := s.tunnels
	s.tunnels = nil
	s.mu.Unlock()
	for _, t := range tunnels {
		t.mu.Lock()
		if t.open != nil {
			t.open.Close()
			t.open = nil
		}
		t.mu.Unlock()
	}
}

// address describes where conn points, as user@host:port/database, with the SSH server it goes through.
func address(conn config.Connection) string {
	s := conn.Host
	if conn.User != "" {
		s = conn.User + "@" + s
	}
	if conn.Port != 0 {
		s += ":" + strconv.Itoa(conn.Port)
	}
	if conn.Database != "" {
		s += "/" + conn.Database
	}
	if conn.SSH.Host != "" {
		s += " via " + conn.SSH.Destination()
	}
	return s
}

func colorDot(c *ui.Context, color ui.Color) {
	ui.Column(c).Width(7).Height(7).Radius(4).Shrink(0).Background(color)
}

func readOnlyNote(conn config.Connection) string {
	if conn.ReadOnly {
		return " · read-only"
	}
	return ""
}

// chosenLook marks the item of the tree that is chosen with a quiet fill, where the theme's own would fill it with the accent.
func chosenLook(c *ui.Context, item ui.Element, chosen bool) {
	if chosen {
		item.Background(c.Theme().SurfacePressed)
	}
}
