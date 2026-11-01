package powersrc

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// parsePmsetBatt reads the first line of `pmset -g batt`, which is one of:
//
//	Now drawing from 'AC Power'
//	Now drawing from 'Battery Power'
//
// It returns true when drawing from battery. An unrecognised first line is
// an error (callers map it to mains via SampleOnBattery).
func parsePmsetBatt(out string) (bool, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Now drawing from") {
			continue
		}
		switch {
		case strings.Contains(line, "'Battery Power'"):
			return true, nil
		case strings.Contains(line, "'AC Power'"):
			return false, nil
		default:
			return false, fmt.Errorf("pmset: unrecognised source line %q", line)
		}
	}
	return false, fmt.Errorf("pmset: no 'Now drawing from' line in output")
}

// Level is what the lid guard needs from `pmset -g batt`: the source and the
// charge. Percent is -1 when the output carries no percentage (a Mac with no
// battery), which the guard reads as "unknown" — never as low.
type Level struct {
	OnBattery bool
	Percent   int
}

var pctRE = regexp.MustCompile(`\t(\d{1,3})%;`)

func parsePmsetBattLevel(out string) (Level, error) {
	onBatt, err := parsePmsetBatt(out)
	if err != nil {
		return Level{}, err
	}
	lv := Level{OnBattery: onBatt, Percent: -1}
	if m := pctRE.FindStringSubmatch(out); m != nil {
		lv.Percent, _ = strconv.Atoi(m[1]) // \d{1,3} always parses
	}
	return lv, nil
}
