package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// jobs runs one long task at a time, a dump or a SQL file, and shows how it goes at the foot of the sidebar.
type jobs struct {
	update func(fn func())

	running *job
	// last is the outcome of the task that ended last, until dismissed.
	last *jobOutcome
}

type job struct {
	// label says what runs, such as "Dumping public".
	label  string
	cancel context.CancelFunc
	// progress returns how far the task is, as text and as a fraction, which is negative when the end is not known.
	progress func() (text string, fraction float64)
}

type jobOutcome struct {
	text string
	// detail shows as the tooltip: the whole of an error, or where a file went.
	detail string
	failed bool
}

// start runs run off the UI thread, unless a task is already running, which it reports.
// run returns what to show once it is done, with a detail for the tooltip.
// canceled is what to show when the user cancels, and done, if not nil, follows on the UI thread.
func (j *jobs) start(label string, progress func() (string, float64), canceled string, run func(ctx context.Context) (text, detail string, err error), done func(err error)) bool {
	if j.running != nil {
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	j.running, j.last = &job{label: label, cancel: cancel, progress: progress}, nil
	// The progress is read as the view builds, and this only asks for the frames that show it moving.
	go func() {
		tick := time.NewTicker(300 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				j.update(func() {})
			}
		}
	}()
	go func() {
		text, detail, err := run(ctx)
		cancel()
		j.update(func() {
			j.running = nil
			switch {
			case errors.Is(err, context.Canceled):
				j.last = &jobOutcome{text: canceled}
			case err != nil:
				// A tool can print many lines, of which the first says what went wrong.
				msg := errorText(err)
				first, _, _ := strings.Cut(msg, "\n")
				j.last = &jobOutcome{text: label + " failed: " + first, detail: msg, failed: true}
			default:
				j.last = &jobOutcome{text: text, detail: detail}
			}
			if done != nil {
				done(err)
			}
		})
	}()
	return true
}

func (j *jobs) view(c *ui.Context) {
	if j.running == nil && j.last == nil {
		return
	}
	t := c.Theme()
	hairline(c)
	ui.Column(c).FillWidth().Padding(t.Space(2), t.Space(2), t.Space(2), t.Space(3)).Gap(t.Space(1)).Children(func() {
		if run := j.running; run != nil {
			text, fraction := run.progress()
			ui.Row(c).FillWidth().Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
				if fraction < 0 {
					ui.Spinner(c)
				}
				ui.Text(c, run.label+" · "+text).FontSize(12).TextColor(t.TextMuted).Grow(1)
				iconButton(c, iconStop, "Cancel").OnClick(run.cancel)
			})
			if fraction >= 0 {
				ui.Progress(c, fraction).FillWidth().Label(run.label)
			}
			return
		}
		ui.Row(c).FillWidth().Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			color := t.TextMuted
			if j.last.failed {
				color = t.Danger
			}
			ui.Text(c, j.last.text).FontSize(12).TextColor(color).Grow(1).Tooltip(j.last.detail).Selectable()
			iconButton(c, iconClose, "Dismiss").OnClick(func() { j.last = nil })
		})
	})
}
