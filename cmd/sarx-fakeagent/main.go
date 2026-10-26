// sarx-fakeagent is the reference worker used to test sarx and to check adapters.
//
// It behaves like a well-mannered headless agent: it finds its task directory, ticks every item on
// the checklist, appends to the journal, and writes a report. Environment variables make it
// misbehave on purpose, so every engine path can be tested without a real model.
//
//	FAKE_STATUS        report status (default DONE)
//	FAKE_SLEEP         seconds to wait before finishing (default 0)
//	FAKE_FAIL          exit 1 instead of finishing
//	FAKE_ERR_TEXT      text written to stderr
//	FAKE_OUT_TEXT      text written to stdout
//	FAKE_RESULT_FROM   file whose contents become the report
//	FAKE_STDOUT_REPORT print the report to stdout instead of writing result.md
//	FAKE_NO_REPORT     write no report at all and print nothing
//	FAKE_NO_TICK       leave the checklist alone
//	FAKE_WRITE         relative path of a file to create in the working directory
//	FAKE_LOG           file that records each invocation's arguments
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	args := os.Args[1:]
	if p := os.Getenv("FAKE_LOG"); p != "" {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintf(f, "CALL %s\n", strings.Join(args, " "))
			f.Close()
		}
	}
	// A worker that is given its prompt on stdin still has to drain it.
	if st, err := os.Stdin.Stat(); err == nil && st.Mode()&os.ModeCharDevice == 0 {
		io.Copy(io.Discard, os.Stdin)
	}

	dir := os.Getenv("SARX_TASK_DIR")
	if dir == "" {
		fmt.Fprintln(os.Stderr, "fakeagent: SARX_TASK_DIR is not set")
		os.Exit(3)
	}
	if os.Getenv("SARX_WORKER") == "" {
		fmt.Fprintln(os.Stderr, "fakeagent: SARX_WORKER is not set")
		os.Exit(3)
	}

	if s := os.Getenv("FAKE_OUT_TEXT"); s != "" {
		fmt.Println(s)
	}
	if s := os.Getenv("FAKE_ERR_TEXT"); s != "" {
		fmt.Fprintln(os.Stderr, s)
	}
	if rel := os.Getenv("FAKE_WRITE"); rel != "" {
		_ = os.MkdirAll(filepath.Dir(rel), 0o755)
		_ = os.WriteFile(rel, []byte("written by fakeagent\n"), 0o644)
	}
	if os.Getenv("FAKE_NO_TICK") == "" {
		progress := filepath.Join(dir, "progress.md")
		if data, err := os.ReadFile(progress); err == nil {
			s := strings.ReplaceAll(string(data), "- [ ]", "- [x]")
			_ = os.WriteFile(progress, []byte(s), 0o644)
		}
		if f, err := os.OpenFile(filepath.Join(dir, "journal.md"), os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintf(f, "\n## all subtasks\nprogress proven\n")
			f.Close()
		}
	}
	if s := os.Getenv("FAKE_SLEEP"); s != "" {
		if secs, err := strconv.ParseFloat(s, 64); err == nil {
			time.Sleep(time.Duration(secs * float64(time.Second)))
		}
	}
	if os.Getenv("FAKE_FAIL") != "" {
		os.Exit(1)
	}
	if os.Getenv("FAKE_NO_REPORT") != "" {
		return
	}

	report := report()
	if os.Getenv("FAKE_STDOUT_REPORT") != "" {
		fmt.Print(report)
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "result.md"), []byte(report), 0o644)
	fmt.Println("PROBE-DONE")
}

func report() string {
	if p := os.Getenv("FAKE_RESULT_FROM"); p != "" {
		if data, err := os.ReadFile(p); err == nil {
			return string(data)
		}
	}
	status := os.Getenv("FAKE_STATUS")
	if status == "" {
		status = "DONE"
	}
	return "STATUS: " + status + "\n\n## Checks\nPASS: true\n\n## Files\n- none\n\n## Notes\nprobe report\n"
}
