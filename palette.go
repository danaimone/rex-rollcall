package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/danaimone/rex-rollcall/internal/rexcli"
	"github.com/danaimone/rex-rollcall/internal/rows"
	"github.com/danaimone/rex-rollcall/internal/termtext"
)

// rex.ui.pick takes at most 2,000 items:
// https://www.superlogical.com/rex/docs/customize/custom-ui#asking-for-a-choice
const rexPickMaxItems = 2000

const paletteMinDeadlineMS = 4000

type paletteItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	Icon     string `json:"icon"`
	Detail   string `json:"detail"`
}

type paletteChoice struct {
	Row    rows.Row      `json:"row"`
	Target rexcli.Target `json:"target"`
}

type paletteList struct {
	Items       []paletteItem     `json:"items"`
	Choices     map[string]string `json:"choices"`
	Placeholder string            `json:"placeholder"`
}

func newPaletteList(g gathered, self string, now time.Time) (paletteList, error) {
	list := paletteList{Items: []paletteItem{}, Choices: map[string]string{}, Placeholder: palettePlaceholder(g.Warnings)}
	layout := rows.NewLayout(g.Rows, self, 0)
	shown := rows.Filter(g.Rows, rows.All, "", false)
	for _, i := range shown[:min(len(shown), rexPickMaxItems)] {
		r := g.Rows[i]
		t := g.target(r)
		choice, err := json.Marshal(paletteChoice{Row: r, Target: t})
		if err != nil {
			return list, err
		}
		id := strconv.Itoa(i)
		list.Choices[id] = string(choice)
		list.Items = append(list.Items, paletteItem{
			ID:       id,
			Title:    rows.OneLine(r.Title),
			Subtitle: paletteSubtitle(r, layout.Who(r), t.Local),
			Icon:     paletteIcon(r),
			Detail:   paletteDetail(r, now),
		})
	}
	return list, nil
}

func palettePlaceholder(warnings []string) string {
	if len(warnings) == 0 {
		return "Filter sessions"
	}
	return "Filter sessions · ⚠ " + termtext.Truncate(strings.Join(warnings, "  "), 80)
}

func paletteSubtitle(r rows.Row, who string, local bool) string {
	parts := []string{r.Agent}
	if who != "" {
		parts = append(parts, who)
	}
	if r.Dir != "" {
		parts = append(parts, homeRelative(r.Dir, local))
	}
	return strings.Join(parts, " · ")
}

func paletteIcon(r rows.Row) string {
	if r.Live() {
		return "terminal"
	}
	return "clock"
}

func paletteDetail(r rows.Row, now time.Time) string {
	ago := rows.Ago(now, r.Updated)
	switch {
	case !r.Live():
		return ago
	case ago == "now":
		return r.State.String()
	}
	return r.State.String() + " · " + ago
}

func putLaunchingRexOnPath() {
	if _, err := exec.LookPath("rex"); err == nil {
		return
	}
	parent, err := executableOf(os.Getppid())
	if err != nil || !filepath.IsAbs(parent) || filepath.Base(parent) != "rex" {
		return
	}
	os.Setenv("PATH", filepath.Dir(parent)+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func executableOf(pid int) (string, error) {
	if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
		return exe, nil
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	return strings.TrimSpace(string(out)), err
}

func paletteCommand(ctx context.Context, op string, s Settings, a rexcli.Anchor, stdin io.Reader, stdout io.Writer) error {
	putLaunchingRexOnPath()
	switch op {
	case "list":
		var g gathered
		if s.Demo {
			g = demoGathered()
		} else {
			s.DeadlineMS = max(s.DeadlineMS, paletteMinDeadlineMS)
			g = gather(ctx, s)
		}
		list, err := newPaletteList(g, os.Getenv("USER"), time.Now())
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(list)
	case "open":
		var c paletteChoice
		if err := json.NewDecoder(stdin).Decode(&c); err != nil {
			return fmt.Errorf("_palette open: read the choice: %w", err)
		}
		if !c.Row.Live() && (c.Row.Past == nil || len(c.Row.Past.Launch) == 0) {
			return errors.New("_palette open: the choice has no pane to focus and no command to resume")
		}
		return act(ctx, c.Target, a, c.Row, false)
	}
	return fmt.Errorf("_palette: unknown op %q", op)
}
