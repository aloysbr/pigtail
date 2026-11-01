package powersrc

import "testing"

func TestParsePmsetBatt(t *testing.T) {
	const acOut = "Now drawing from 'AC Power'\n -InternalBattery-0 (id=12345)\t100%; charged; 0:00 remaining present: true\n"
	const battOut = "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=12345)\t82%; discharging; 3:41 remaining present: true\n"

	cases := []struct {
		name    string
		out     string
		want    bool
		wantErr bool
	}{
		{"AC power → not on battery", acOut, false, false},
		{"battery power → on battery", battOut, true, false},
		{"unrecognised output → error", "garbage with no source line\n", false, true},
		{"empty output → error", "", false, true},
	}
	for _, c := range cases {
		got, err := parsePmsetBatt(c.out)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", c.name, err, c.wantErr)
			continue
		}
		if err == nil && got != c.want {
			t.Errorf("%s: parsePmsetBatt = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParsePmsetBattLevel(t *testing.T) {
	const battOut = "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=23199843)\t64%; discharging; 1:40 remaining present: true\n"
	const lowOut = "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t9%; discharging; 0:20 remaining present: true\n"
	const acOut = "Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t100%; charged; 0:00 remaining present: true\n"
	const desktopOut = "Now drawing from 'AC Power'\n"
	cases := []struct {
		name    string
		out     string
		want    Level
		wantErr bool
	}{
		{"battery 64%", battOut, Level{OnBattery: true, Percent: 64}, false},
		{"battery 9%", lowOut, Level{OnBattery: true, Percent: 9}, false},
		{"AC 100%", acOut, Level{OnBattery: false, Percent: 100}, false},
		{"desktop, no battery line", desktopOut, Level{OnBattery: false, Percent: -1}, false},
		{"garbage", "nothing here\n", Level{}, true},
	}
	for _, c := range cases {
		got, err := parsePmsetBattLevel(c.out)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", c.name, err, c.wantErr)
			continue
		}
		if err == nil && got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}
