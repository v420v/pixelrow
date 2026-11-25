package app

import "github.com/egoist/mygo/ui"

// The icons are drawn on a 24 by 24 grid and take the color of the text around them.
var (
	iconRun    = filled(`<path d="M7 4.5v15l12-7.5z"/>`)
	iconStop   = filled(`<rect x="6" y="6" width="12" height="12" rx="1.5"/>`)
	iconReload = stroked(`<path d="M20 12a8 8 0 1 1-2.6-5.9"/><path d="M20 4v5h-5"/>`)
	iconClose  = stroked(`<path d="M6 6l12 12M18 6L6 18"/>`)
	iconFile   = stroked(`<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/><path d="M9 13h6M9 17h6"/>`)
	iconRows   = stroked(`<rect x="4" y="5" width="16" height="14" rx="2"/><path d="M4 10h16M4 14.5h16M10 10v9"/>`)
	iconFields = stroked(`<path d="M9 7h11M9 12h11M9 17h11"/><path d="M4.5 7h.01M4.5 12h.01M4.5 17h.01"/>`)
	iconLinks  = stroked(`<rect x="3" y="4" width="7" height="6" rx="1.5"/><rect x="14" y="14" width="7" height="6" rx="1.5"/><path d="M10 7h3a2 2 0 0 1 2 2v5"/>`)
)

func filled(shapes string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor">` + shapes + `</svg>`))
}

func stroked(shapes string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` + shapes + `</svg>`))
}

func iconButton(c *ui.Context, icon *ui.SVG, label string) ui.Element {
	return ghost(c, icon, label, false)
}

// primaryIconButton is iconButton for the one action a bar is for, drawn in the accent color.
func primaryIconButton(c *ui.Context, icon *ui.SVG, label string) ui.Element {
	return ghost(c, icon, label, true)
}

func ghost(c *ui.Context, icon *ui.SVG, label string, accent bool) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Label(label).Tooltip(label).Size(24, 24).Radius(t.Radius).Shrink(0)
	color := t.TextMuted
	switch {
	case accent:
		color = t.Accent
	case b.Hovered():
		color = t.Text
	}
	switch {
	case b.Pressed():
		b.Background(t.SurfacePressed)
	case b.Hovered():
		b.Background(t.SurfaceHover)
	}
	return b.TextColor(color).Children(func() {
		ui.Icon(c, icon).Size(14, 14)
	})
}

// toggleButton is iconButton held down while on, for choosing one of a few views.
func toggleButton(c *ui.Context, icon *ui.SVG, label string, on bool) ui.Element {
	t := c.Theme()
	b := ghost(c, icon, label, false)
	if on {
		b.Background(t.SurfacePressed).TextColor(t.Text)
	}
	return b
}
