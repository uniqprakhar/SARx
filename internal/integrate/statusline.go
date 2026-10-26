package integrate

import (
	"fmt"
	"strings"
	"time"

	"sarx/internal/engine"
	"sarx/internal/proc"
	"sarx/internal/spec"
	"sarx/internal/store"
	"sarx/internal/util"
)

// Statusline renders one segment per live task, for a tool's status bar. It reads only the small
// state, checklist and journal files, never the logs, and returns "" when nothing is running.
func Statusline(root string, now time.Time, color bool) string {
	paint := func(code, s string) string {
		if !color {
			return s
		}
		return "\x1b[" + code + "m" + s + "\x1b[0m"
	}
	var segs []string
	for i, t := range store.List(root) {
		if i >= 40 || len(segs) >= 6 {
			break
		}
		st, err := store.ReadState(t)
		if err != nil || (st.State != spec.Running && st.State != spec.Queued) {
			continue
		}
		mark, tint := "*", "38;5;80"
		switch {
		case st.SupervisorPID > 0 && !proc.Alive(st.SupervisorPID):
			mark, tint = "x", "31"
		case engine.WorkerBlocked(t):
			mark, tint = "~", "38;5;208"
		case st.State == spec.Queued:
			mark, tint = ".", "38;5;250"
		}
		done, total := engine.Ticks(t)
		step := engine.LastStep(t)
		if step == "" {
			step = t.ID
		}
		if len(step) > 34 {
			step = step[:34]
		}
		progress := ""
		if total > 0 {
			progress = ":" + paint("38;5;154", fmt.Sprintf("%d/%d", done, total))
		}
		age := util.Age(now.Unix() - st.Started)
		segs = append(segs, paint(tint, mark)+paint("38;5;250", st.Agent)+progress+" "+paint("38;5;240", step)+" "+paint("38;5;220", age))
	}
	if len(segs) == 0 {
		return ""
	}
	return paint("38;5;240", "sarx") + " " + strings.Join(segs, "   ")
}
