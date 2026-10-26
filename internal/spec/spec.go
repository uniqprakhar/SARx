// Package spec defines the data types of the sarx open specification (see spec/SPEC.md).
package spec

// Task states.
const (
	Queued    = "QUEUED"
	Running   = "RUNNING"
	Done      = "DONE"
	DoneNotes = "DONE_WITH_NOTES"
	Blocked   = "BLOCKED"
	Failed    = "FAILED"
	Quota     = "QUOTA"
	Crashed   = "CRASHED"
	Timeout   = "TIMEOUT"
	NoReport  = "NO_REPORT"
	Unmarked  = "UNMARKED"
	Cancelled = "CANCELLED"
	Stalled   = "STALLED" // derived, never stored
)

// Roles.
const (
	Operator = "operator"
	Builder  = "builder"
	Reviewer = "reviewer"
	Fallback = "fallback"
)

// Roles lists every role in display order.
var Roles = []string{Operator, Builder, Reviewer, Fallback}

// IsLive reports whether a state means the task has not finished.
func IsLive(state string) bool {
	return state == Queued || state == Running || state == Stalled
}

// IsSuccess reports whether a state counts as a finished, usable outcome.
func IsSuccess(state string) bool { return state == Done || state == DoneNotes }

// Checks counts PASS and FAIL lines.
type Checks struct {
	Pass int `json:"pass"`
	Fail int `json:"fail"`
}

// CheckDetail describes one failed check.
type CheckDetail struct {
	Index int    `json:"index"`
	Exit  int    `json:"exit"`
	Tail  string `json:"tail,omitempty"`
}

// Verified is the engine's own check result.
type Verified struct {
	Ran     bool          `json:"ran"`
	Pass    int           `json:"pass"`
	Fail    int           `json:"fail"`
	Details []CheckDetail `json:"details,omitempty"`
}

// State is the contents of state.json.
type State struct {
	Version       int       `json:"version"`
	ID            string    `json:"id"`
	Run           string    `json:"run,omitempty"`
	Title         string    `json:"title,omitempty"`
	State         string    `json:"state"`
	Agent         string    `json:"agent"`
	Role          string    `json:"role"`
	Adapter       string    `json:"adapter,omitempty"`
	Mode          string    `json:"mode"`
	Exit          int       `json:"exit"`
	Seconds       int64     `json:"seconds"`
	Started       int64     `json:"started"`
	Finished      int64     `json:"finished,omitempty"`
	PID           int       `json:"pid,omitempty"`
	SupervisorPID int       `json:"supervisor_pid,omitempty"`
	Session       string    `json:"session,omitempty"`
	Resumed       string    `json:"resumed,omitempty"`
	Attempt       int       `json:"attempt"`
	Subtasks      int       `json:"subtasks"`
	TaskDir       string    `json:"task_dir"`
	Task          string    `json:"task"`
	Result        string    `json:"result"`
	Progress      string    `json:"progress"`
	Journal       string    `json:"journal"`
	Stdout        string    `json:"stdout,omitempty"`
	Stderr        string    `json:"stderr,omitempty"`
	Lock          string    `json:"lock,omitempty"`
	Isolation     string    `json:"isolation,omitempty"`
	Worktree      string    `json:"worktree,omitempty"`
	Branch        string    `json:"branch,omitempty"`
	Claimed       Checks    `json:"claimed_checks"`
	Verified      *Verified `json:"verified,omitempty"`
	Warnings      []string  `json:"warnings,omitempty"`
	Note          string    `json:"note,omitempty"`
	RetryOf       string    `json:"retry_of,omitempty"`
}

// Event is one line of events.jsonl.
type Event struct {
	TS   int64  `json:"ts"`
	Type string `json:"type"`
	Msg  string `json:"msg,omitempty"`
}

// Result is the contents of result.json.
type Result struct {
	Status     string   `json:"status"`
	Source     string   `json:"source"`
	Resolution string   `json:"resolution,omitempty"`
	Claimed    Checks   `json:"claimed_checks"`
	Files      []string `json:"files,omitempty"`
	Notes      string   `json:"notes,omitempty"`
}

// Options is the contents of dispatch.json: how a task was dispatched.
type Options struct {
	Agent       string   `json:"agent,omitempty"`
	Role        string   `json:"role,omitempty"`
	ReadOnly    bool     `json:"readonly,omitempty"`
	Timeout     int      `json:"timeout,omitempty"`
	Lock        string   `json:"lock,omitempty"`
	Isolation   string   `json:"isolation,omitempty"`
	Resuming    bool     `json:"resuming,omitempty"`
	Resume      string   `json:"resume,omitempty"`
	SessionID   string   `json:"session_id,omitempty"`
	AllowImages bool     `json:"allow_images,omitempty"`
	NoVerify    bool     `json:"no_verify,omitempty"`
	Extra       []string `json:"extra,omitempty"`
	RetryOf     string   `json:"retry_of,omitempty"`
}
