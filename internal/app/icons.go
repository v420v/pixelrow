package app

import "github.com/egoist/mygo/ui"

// The icons are drawn on a 24 by 24 grid and take the color of the text around them.
var (
	iconRun    = filled(`<path d="M7 4.5v15l12-7.5z"/>`)
	iconStop   = filled(`<rect x="6" y="6" width="12" height="12" rx="1.5"/>`)
	iconReload = stroked(`<path d="M20 12a8 8 0 1 1-2.6-5.9"/><path d="M20 4v5h-5"/>`)
	iconClose  = stroked(`<path d="M6 6l12 12M18 6L6 18"/>`)
	iconFile   = stroked(`<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/><path d="M9 13h6M9 17h6"/>`)
)

func filled(shapes string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor">` + shapes + `</svg>`))
}

func stroked(shapes string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` + shapes + `</svg>`))
}

func iconButton(c *ui.Context, icon *ui.SVG, label string) ui.Element {
	return slim(c, ui.Button(c, ""), icon, label)
}

func primaryIconButton(c *ui.Context, icon *ui.SVG, label string) ui.Element {
	return slim(c, ui.PrimaryButton(c, ""), icon, label)
}

func slim(c *ui.Context, button ui.Element, icon *ui.SVG, label string) ui.Element {
	return button.Label(label).Tooltip(label).Size(24, 24).Padding(0).Justify(ui.Center).Children(func() {
		ui.Icon(c, icon).Size(14, 14)
	})
}
