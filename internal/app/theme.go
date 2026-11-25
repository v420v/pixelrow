package app

import "github.com/egoist/mygo/ui"

// The look is quiet on purpose: neutral grays, hairline borders and small type, so that the data is what stands out.
// Panels (the sidebar, the tab bar, the status bar) sit on Surface, a shade off the Background the editor and results use.
var (
	lightTheme = themed(ui.LightTheme(), palette{
		background: "#fcfcfc", surface: "#f1f1f2", hover: "#e6e6e8", pressed: "#dcdcdf",
		border: "#dedee1", text: "#2b2d31", muted: "#7b7e86",
		accent: "#4a6fe3", accentHover: "#3f61cc", accentPressed: "#3654b5",
		danger: "#d1453b", warning: "#b7791f", success: "#2f9461",
	})
	darkTheme = themed(ui.DarkTheme(), palette{
		background: "#1e2024", surface: "#25272c", hover: "#2f3238", pressed: "#383b43",
		border: "#32353c", text: "#d6d9df", muted: "#8a8f99",
		accent: "#6d8ef2", accentHover: "#84a0f5", accentPressed: "#5b7bdb",
		danger: "#e5645a", warning: "#d9a441", success: "#55b987",
	})
)

type palette struct {
	background, surface, hover, pressed string
	border, text, muted                 string
	accent, accentHover, accentPressed  string
	danger, warning, success            string
}

func themed(t *ui.Theme, p palette) *ui.Theme {
	t.Background, t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex(p.background), ui.Hex(p.surface), ui.Hex(p.hover), ui.Hex(p.pressed)
	t.Border, t.Text, t.TextMuted = ui.Hex(p.border), ui.Hex(p.text), ui.Hex(p.muted)
	t.Accent, t.AccentHover, t.AccentPressed = ui.Hex(p.accent), ui.Hex(p.accentHover), ui.Hex(p.accentPressed)
	t.Danger, t.Warning, t.Success = ui.Hex(p.danger), ui.Hex(p.warning), ui.Hex(p.success)
	t.Selection, t.Focus = t.Accent.Alpha(0.28), t.Accent.Alpha(0.6)
	t.Radius, t.FontSize = 4, 13
	return t
}

func useTheme(c *ui.Context) {
	if c.Theme().Dark {
		c.SetTheme(darkTheme)
	} else {
		c.SetTheme(lightTheme)
	}
}

// Sizes shared by the panels, so their edges line up across the window.
const (
	barHeight = 34
	rowHeight = 26
	smallText = 12
	tinyText  = 11
)

// monospace is the font of data: the cells of a result, and names read as code.
const monospace = "monospace"

func heading(c *ui.Context, text string) ui.Element {
	return ui.Text(c, text).FontSize(tinyText).FontWeight(600).LetterSpacing(0.4).TextColor(c.Theme().TextMuted).SingleLine()
}

func hairline(c *ui.Context) ui.Element {
	return ui.Column(c).FillWidth().Height(1).Shrink(0).Background(c.Theme().Border)
}
