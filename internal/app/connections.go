package app

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"

	"db/internal/config"
	"db/internal/driver"
)

type connections struct {
	path    string
	drivers map[string]driver.Driver
	update  func(fn func())
	// openFile opens the config file in the user's editor.
	openFile func(path string) error
	// onConnect is called with the connection the user chose to open.
	onConnect func(conn config.Connection)

	loaded  bool
	list    []config.Connection
	loadErr error
	rows    ui.ListState
	// selected is the index of the connection shown, or -1 for none.
	selected int
	// shown is the index the status beside the buttons belongs to.
	shown int

	status  string
	failed  bool
	testing bool
	tests   int
}

func newConnections(path string, drivers map[string]driver.Driver, update func(fn func()), openFile func(string) error, onConnect func(config.Connection)) *connections {
	s := &connections{
		path: path,
		drivers: drivers,
		update: update,
		openFile: openFile,
		onConnect: onConnect,
		selected: -1,
		shown: -1,
	}
	s.rows.Selected = &s.selected
	return s
}

func (s *connections) view(c *ui.Context) {
	t := c.Theme()
	if !s.loaded {
		s.loaded = true
		s.reload()
	}

	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(260).Children(func() {
			ui.Row(c).FillWidth().Padding(t.Space(2)).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Connections").Bold().SingleLine()
				ui.Spacer(c)
				ui.Button(c, "Reload").OnClick(s.reload)
			})
			ui.Divider(c)
			if ui.List(c, &s.rows, len(s.list), func(i int) {
				// Muted text does not show on the accent color of the chosen row.
				muted := t.TextMuted
				if i == s.selected {
					muted = t.AccentText.Alpha(0.75)
				}
				ui.Column(c).Padding(t.Space(2)).Gap(2).Children(func() {
					ui.Text(c, s.list[i].Name).SingleLine()
					ui.Text(c, address(s.list[i])).TextColor(muted).FontSize(12).SingleLine()
				})
			}).Grow(1).FillWidth().Submitted() {
				s.connect()
			}
		})
		ui.Divider(c)
		ui.Column(c).Grow(1).Padding(t.Space(6)).Gap(t.Space(4)).Children(func() {
			s.detailsView(c)
		})
	})
}

func (s *connections) detailsView(c *ui.Context) {
	t := c.Theme()
	if s.selected != s.shown {
		s.shown = s.selected
		s.tests++
		s.testing = false
		s.setStatus("", false)
	}

	switch {
	case s.loadErr != nil:
		ui.Text(c, "The config file could not be read").FontSize(20).Bold()
		ui.Text(c, s.loadErr.Error()).TextColor(t.Danger).Selectable()
	case len(s.list) == 0:
		ui.Text(c, "No connections yet").FontSize(20).Bold()
		ui.Text(c, "Add a [connections.<name>] table to the config file, then reload.").TextColor(t.TextMuted)
	case s.selected < 0 || s.selected >= len(s.list):
		ui.Text(c, "Choose a connection").FontSize(20).Bold()
	default:
		s.connectionView(c, s.list[s.selected])
	}

	ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
		ui.Text(c, s.path).TextColor(t.TextMuted).FontSize(12).Selectable()
		ui.Button(c, "Open Config File").OnClick(s.openConfig)
	})
}

func (s *connections) connectionView(c *ui.Context, conn config.Connection) {
	t := c.Theme()
	ui.Text(c, conn.Name).FontSize(20).Bold().SingleLine()

	password := "Not set"
	if conn.Password != "" {
		password = "••••••••"
	}
	port := "Default"
	if conn.Port != 0 {
		port = strconv.Itoa(conn.Port)
	}
	fields := [][2]string{
		{"Kind", conn.Kind}, {"Host", conn.Host}, {"Port", port},
		{"User", conn.User}, {"Password", password}, {"Database", conn.Database},
	}
	ui.Column(c).Gap(t.Space(2)).Children(func() {
		for _, f := range fields {
			ui.Row(c).Gap(t.Space(3)).Children(func() {
				ui.Text(c, f[0]).TextColor(t.TextMuted).Width(80)
				ui.Text(c, f[1]).Selectable()
			})
		}
	})

	ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
		ui.PrimaryButton(c, "Connect").OnClick(s.connect)
		ui.Button(c, "Test").Disabled(s.testing).OnClick(s.test)
		switch {
		case s.testing:
			ui.Spinner(c)
			ui.Text(c, "Connecting…").TextColor(t.TextMuted)
		case s.status != "":
			color := t.TextMuted
			if s.failed {
				color = t.Danger
			}
			ui.Text(c, s.status).TextColor(color).Selectable()
		}
	})
}

// reload reads the config file again, keeping the connection chosen by its name.
func (s *connections) reload() {
	var chosen string
	if s.selected >= 0 && s.selected < len(s.list) {
		chosen = s.list[s.selected].Name
	}
	if err := config.Create(s.path); err != nil {
		s.list, s.loadErr, s.selected = nil, err, -1
		return
	}
	s.list, s.loadErr = config.Load(s.path)
	s.selected, s.shown = -1, -1
	s.rows = ui.ListState{Selected: &s.selected}
	for i, conn := range s.list {
		if conn.Name == chosen {
			s.selected = i
		}
	}
	if s.selected < 0 && len(s.list) > 0 {
		s.selected = 0
	}
}

func (s *connections) openConfig() {
	if err := s.openFile(s.path); err != nil {
		s.setStatus("Could not open the config file: "+err.Error(), true)
	}
}

func (s *connections) setStatus(text string, failed bool) {
	s.status, s.failed = text, failed
}

// test connects to the chosen connection once and reports how it went.
func (s *connections) test() {
	if s.selected < 0 || s.selected >= len(s.list) {
		return
	}
	conn := s.list[s.selected]
	s.testing = true
	s.tests++
	id := s.tests
	go func() {
		start := time.Now()
		err := ping(s.drivers, conn)
		elapsed := time.Since(start)
		s.update(func() {
			if id != s.tests {
				return
			}
			s.testing = false
			if err != nil {
				s.setStatus(errorText(err), true)
			} else {
				s.setStatus("Connected in "+formatElapsed(elapsed)+".", false)
			}
		})
	}()
}

func (s *connections) connect() {
	if s.selected >= 0 && s.selected < len(s.list) {
		s.onConnect(s.list[s.selected])
	}
}

// ping opens conn once and closes it.
func ping(drivers map[string]driver.Driver, conn config.Connection) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := open(ctx, drivers, conn)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Ping(ctx)
}

// open connects to conn.
func open(ctx context.Context, drivers map[string]driver.Driver, conn config.Connection) (driver.Conn, error) {
	drv := drivers[conn.Kind]
	if drv == nil {
		return nil, fmt.Errorf("unknown kind %q of connection %q", conn.Kind, conn.Name)
	}
	return drv.Open(ctx, driver.Config{
		Host:     conn.Host,
		Port:     conn.Port,
		User:     conn.User,
		Password: conn.Password,
		Database: conn.Database,
	})
}

// address describes where conn points, as user@host:port/database.
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
	return s
}

