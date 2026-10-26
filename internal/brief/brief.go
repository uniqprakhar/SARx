// Package brief parses a task brief (task.md) as described in spec/SPEC.md section 3.
package brief

import (
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// Brief is a parsed task.md.
type Brief struct {
	Title    string
	Front    map[string]string
	Goal     string
	Subtasks []string // text of each "- [ ]" item
	Checks   []string // one shell snippet per fenced block under "Done when"
	Scope    []string // path patterns
	Raw      string
}

var (
	headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*$`)
	subtaskRe = regexp.MustCompile(`^\s*[-*]\s+\[[ xX]\]\s+(.*\S)\s*$`)
	bulletRe  = regexp.MustCompile(`^\s*[-*]\s+(.*\S)\s*$`)
	tickRe    = regexp.MustCompile("`([^`]+)`")
	imageRe   = regexp.MustCompile("(?i)[^\\s`\"'()<>]+\\.(?:png|jpe?g|gif|webp|bmp|tiff?|heic)\\b")
)

// Parse reads a brief. It never fails: missing sections are simply empty.
func Parse(text string) Brief {
	b := Brief{Front: map[string]string{}, Raw: text}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	i := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for j := 1; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == "---" {
				for _, l := range lines[1:j] {
					if k, v, ok := strings.Cut(l, ":"); ok {
						b.Front[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"'`)
					}
				}
				i = j + 1
				break
			}
		}
	}

	section := ""
	inFence := false
	var fence []string
	var goal []string
	for ; i < len(lines); i++ {
		line := lines[i]
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			if inFence {
				if section == "done when" {
					b.Checks = append(b.Checks, strings.Join(fence, "\n"))
				}
				fence = nil
				inFence = false
			} else {
				inFence = true
				fence = nil
			}
			continue
		}
		if inFence {
			fence = append(fence, line)
			continue
		}
		if m := headingRe.FindStringSubmatch(line); m != nil {
			if len(m[1]) == 1 {
				if b.Title == "" {
					b.Title = m[2]
				}
				continue
			}
			section = strings.ToLower(m[2])
			continue
		}
		switch section {
		case "goal":
			goal = append(goal, line)
		case "subtasks":
			if m := subtaskRe.FindStringSubmatch(line); m != nil {
				b.Subtasks = append(b.Subtasks, m[1])
			}
		case "scope":
			if m := bulletRe.FindStringSubmatch(line); m != nil {
				if ticks := tickRe.FindAllStringSubmatch(m[1], -1); len(ticks) > 0 {
					for _, t := range ticks {
						b.Scope = append(b.Scope, strings.TrimSpace(t[1]))
					}
				} else {
					b.Scope = append(b.Scope, strings.TrimSpace(m[1]))
				}
			}
		}
	}
	b.Goal = strings.TrimSpace(strings.Join(goal, "\n"))
	return b
}

// Images returns the image file names a brief mentions.
func Images(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range imageRe.FindAllString(text, -1) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

// ScopeMatcher returns a function reporting whether a slash-separated, project-relative path is
// inside the brief's Scope. It returns nil when the brief has no Scope section.
func (b Brief) ScopeMatcher() func(string) bool {
	if len(b.Scope) == 0 {
		return nil
	}
	var res []*regexp.Regexp
	for _, p := range b.Scope {
		p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
		p = strings.TrimPrefix(p, "./")
		if p == "" {
			continue
		}
		res = append(res, globToRegexp(p))
	}
	return func(path string) bool {
		path = strings.TrimPrefix(strings.ReplaceAll(path, "\\", "/"), "./")
		for _, re := range res {
			if re.MatchString(path) {
				return true
			}
		}
		return false
	}
}

func globToRegexp(p string) *regexp.Regexp {
	var sb strings.Builder
	if runtime.GOOS == "windows" {
		sb.WriteString("(?i)")
	}
	sb.WriteString("^")
	isGlob := strings.ContainsAny(p, "*?")
	isDir := strings.HasSuffix(p, "/")
	for i := 0; i < len(p); i++ {
		switch c := p[i]; c {
		case '*':
			if i+1 < len(p) && p[i+1] == '*' {
				sb.WriteString(".*")
				i++
			} else {
				sb.WriteString("[^/]*")
			}
		case '?':
			sb.WriteString("[^/]")
		default:
			sb.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	switch {
	case isDir:
		sb.WriteString(".*")
	case !isGlob:
		sb.WriteString("(?:/.*)?")
	}
	sb.WriteString("$")
	return regexp.MustCompile(sb.String())
}
