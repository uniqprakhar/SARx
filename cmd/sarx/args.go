package main

import (
	"fmt"
	"strings"
)

// args is the result of parsing one command's arguments. Flags may appear before, between or after
// positional arguments, and everything after a bare "--" is passed through untouched.
type args struct {
	b    map[string]bool
	s    map[string]string
	l    map[string][]string
	pos  []string
	rest []string
}

type flags struct {
	bools, strs, lists []string
	split              []string // list flags whose values may be comma separated
	alias              map[string]string
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (f flags) parse(argv []string) (*args, error) {
	out := &args{b: map[string]bool{}, s: map[string]string{}, l: map[string][]string{}}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			out.rest = argv[i+1:]
			break
		}
		if len(a) < 2 || a[0] != '-' {
			out.pos = append(out.pos, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		val, hasVal := "", false
		if k := strings.Index(name, "="); k >= 0 {
			name, val, hasVal = name[:k], name[k+1:], true
		}
		if full, ok := f.alias[name]; ok {
			name = full
		}
		switch {
		case has(f.bools, name):
			out.b[name] = !hasVal || (val != "false" && val != "0" && val != "no")
		case has(f.strs, name), has(f.lists, name):
			if !hasVal {
				if i+1 >= len(argv) {
					return nil, fmt.Errorf("--%s needs a value", name)
				}
				i++
				val = argv[i]
			}
			if has(f.lists, name) {
				if has(f.split, name) {
					for _, p := range strings.Split(val, ",") {
						if p = strings.TrimSpace(p); p != "" {
							out.l[name] = append(out.l[name], p)
						}
					}
				} else if val != "" {
					out.l[name] = append(out.l[name], val)
				}
			} else {
				out.s[name] = val
			}
		default:
			return nil, fmt.Errorf("unknown option %s (see: sarx help)", a)
		}
	}
	return out, nil
}
