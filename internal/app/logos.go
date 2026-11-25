package app

import (
	"embed"

	"github.com/egoist/mygo/ui"
)

//go:embed logos/*.svg
var logoFiles embed.FS

// logos maps a connection's kind to the mark of its database. A kind without a file has none.
var logos = map[string]*ui.SVG{}

func init() {
	entries, _ := logoFiles.ReadDir("logos")
	for _, entry := range entries {
		data, err := logoFiles.ReadFile("logos/" + entry.Name())
		if err != nil {
			continue
		}
		name := entry.Name()
		logos[name[:len(name)-len(".svg")]] = ui.MustParseSVG(data)
	}
}

const logoSize = 13

// logoColors holds the color each database's mark is known by. A kind left out takes the color of the text.
var logoColors = map[string]string{
	"postgres": "#4169e1",
	"mysql":    "#4479a1",
}

// logoColorsDark lightens the marks that are too dim on the dark background to tell apart.
var logoColorsDark = map[string]string{
	"postgres": "#6f8ff0",
	"mysql":    "#5d9bc4",
}

// logo builds the mark of the database of kind, or leaves its room empty so that names stay lined up.
func logo(c *ui.Context, kind string) {
	if svg := logos[kind]; svg != nil {
		t := c.Theme()
		color := t.Text.Alpha(0.8)
		colors := logoColors
		if t.Dark {
			colors = logoColorsDark
		}
		if hex := colors[kind]; hex != "" {
			color = ui.Hex(hex)
		}
		ui.Icon(c, svg).Size(logoSize, logoSize).Shrink(0).TextColor(color).Label(kind)
		return
	}
	ui.Column(c).Size(logoSize, logoSize).Shrink(0)
}
