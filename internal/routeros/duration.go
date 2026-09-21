package routeros

import (
	"regexp"
	"strconv"
)

var (
	hmsRe = regexp.MustCompile(`^(\d+):(\d+):(\d+)$`)
	// ms is an alternative of its own, listed first, so the 930 of "24s930ms" is
	// not read as 930 minutes. A bare number is the legacy seconds spelling.
	durRe    = regexp.MustCompile(`(\d+)(ms|w|d|h|m|s)`)
	digitsRe = regexp.MustCompile(`^\d+$`)
)

// DurationSeconds reads a RouterOS duration into seconds: "01:02:03",
// "2w1d2h3m4s", "24s930ms" (milliseconds dropped) and a bare "90". Anything
// else is 0.
//
// Here rather than in a collector because it is RouterOS's own spelling, and
// more than one package reads it: the routing collector's session uptimes and
// Config Management's check that a router rebooted when it should have.
func DurationSeconds(s string) int {
	if s == "" {
		return 0
	}
	if m := hmsRe.FindStringSubmatch(s); m != nil {
		return atoi(m[1])*3600 + atoi(m[2])*60 + atoi(m[3])
	}
	if digitsRe.MatchString(s) {
		return atoi(s)
	}
	scale := map[string]int{"w": 604800, "d": 86400, "h": 3600, "m": 60, "s": 1, "ms": 0}
	sec := 0
	for _, m := range durRe.FindAllStringSubmatch(s, -1) {
		sec += atoi(m[1]) * scale[m[2]]
	}
	return sec
}

// atoi is for digit runs the patterns above already matched.
func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
