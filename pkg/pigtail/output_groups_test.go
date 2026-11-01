package pigtail

import (
	"fmt"
	"testing"
)

func out(seq int64, kind, path string, ts int64) SubjectOutput {
	yes := true
	return SubjectOutput{Seq: seq, ID: fmt.Sprintf("out_%d", seq), Kind: kind, Path: path, Title: fmt.Sprintf("title %d", seq), TS: ts, Exists: &yes}
}

func TestGroupOutputsToursSortedLatestHighest(t *testing.T) {
	base := "/o/_outputs/app-macos-atlas/mockup/"
	in := []SubjectOutput{
		out(1, "mockup", base+"t2/index.html", 20),
		out(2, "mockup", base+"t10/index.html", 100),
		out(3, "mockup", base+"t1/index.html", 10),
		out(4, "mockup", base+"t3/index.html", 30),
	}
	in[1].ServedURL = "http://x/t10/"
	groups := GroupOutputs(in)
	if len(groups) != 1 {
		t.Fatalf("groups = %d; want 1: %+v", len(groups), groups)
	}
	g := groups[0]
	if g.Key != "/o/_outputs/app-macos-atlas/mockup" || g.Name != "app-macos-atlas" || g.Kind != "mockup" || g.FirstSeq != 1 {
		t.Fatalf("group = %+v", g)
	}
	var ns []int
	for _, tr := range g.Tours {
		ns = append(ns, tr.N)
	}
	if fmt.Sprint(ns) != "[1 2 3 10]" {
		t.Fatalf("tours N = %v; want [1 2 3 10] (numeric, latest last)", ns)
	}
	last := g.Tours[len(g.Tours)-1]
	if last.OutputID != "out_2" || last.Label != "t10" || last.ServedURL != "http://x/t10/" || !last.Exists || last.TS != 100 || last.Path != base+"t10/index.html" || last.Title != "title 2" {
		t.Fatalf("latest tour = %+v", last)
	}
}

func TestGroupOutputsTieKeepsBothLatestByTS(t *testing.T) {
	base := "/o/x/mockup/"
	groups := GroupOutputs([]SubjectOutput{
		out(1, "mockup", base+"t2/index.html", 50),
		out(2, "mockup", base+"t2/alt.html", 40),
	})
	if len(groups) != 1 || len(groups[0].Tours) != 2 {
		t.Fatalf("groups = %+v", groups)
	}
	if groups[0].Tours[1].OutputID != "out_1" {
		t.Fatalf("tie: the most recent by ts must be the latest (last): %+v", groups[0].Tours)
	}
}

func TestGroupOutputsSuffixedTour(t *testing.T) {
	groups := GroupOutputs([]SubjectOutput{
		out(1, "mockup", "/o/slug/mockup/t42-teams/index.html", 1),
		out(2, "mockup", "/o/slug/mockup/t7_alt/index.html", 2),
		out(3, "mockup", "/o/slug/mockup/t5x/index.html", 3), // not a round: no separator
	})
	if len(groups) != 2 {
		t.Fatalf("groups = %d; want 2 (rounds + the t5x file alone): %+v", len(groups), groups)
	}
	g := groups[0]
	if len(g.Tours) != 2 || g.Tours[0].N != 7 || g.Tours[1].N != 42 || g.Tours[1].Label != "t42-teams" {
		t.Fatalf("suffixed tours = %+v", g.Tours)
	}
	if groups[1].Tours[0].N != 0 {
		t.Fatalf("t5x read as a round: %+v", groups[1])
	}
}

func TestGroupOutputsSkipsGenericNames(t *testing.T) {
	for path, want := range map[string]string{
		"/o/_outputs/app-macos-atlas/mockup/t23/index.html": "app-macos-atlas",
		"/o/prototypes/my-slug/t3/index.html":               "my-slug",
		"/o/my-slug/report/t2/index.html":                   "my-slug",
		"/o/my-slug/reports/rounds/t2/index.html":           "my-slug",
		"/o/_outputs/mockups/t1/index.html":                 "o",
	} {
		groups := GroupOutputs([]SubjectOutput{out(1, "report", path, 1)})
		if len(groups) != 1 || groups[0].Name != want {
			t.Errorf("%s → %+v; want name %q", path, groups, want)
		}
	}
}

func TestGroupOutputsNonTourIsSingleGroup(t *testing.T) {
	groups := GroupOutputs([]SubjectOutput{
		out(1, "report", "/o/my-slug/report/summary.html", 5),
		out(2, "report", "/o/my-slug/report/t1/index.html", 6),
	})
	if len(groups) != 2 {
		t.Fatalf("groups = %+v", groups)
	}
	g := groups[0]
	if g.FirstSeq != 1 || len(g.Tours) != 1 || g.Tours[0].N != 0 || g.Tours[0].Label != "summary.html" || g.Key != "/o/my-slug/report/summary.html" {
		t.Fatalf("non-round group = %+v", g)
	}
	if groups[1].FirstSeq != 2 {
		t.Fatalf("groups out of first-registration order: %+v", groups)
	}
}

func TestGroupOutputsIgnoresCapturesAndPRs(t *testing.T) {
	purged := out(4, "mockup", "/o/s/mockup/t2/index.html", 4)
	p := int64(9)
	purged.PurgedAt = &p
	groups := GroupOutputs([]SubjectOutput{
		out(1, "capture", "/o/s/mockup/t1/shot.png", 1),
		{Seq: 2, Kind: "pr", URL: "https://github.com/o/r/pull/1"},
		purged,
		{Seq: 5, Kind: "mockup", URL: "http://localhost:3000/"},
	})
	if len(groups) != 0 {
		t.Fatalf("groups = %+v; want none (only non-purged mockup/report with a path)", groups)
	}
	if GroupOutputs(nil) == nil {
		t.Fatal("GroupOutputs(nil) = nil; want an empty slice")
	}
}

func TestGroupOutputsHTMLIsSingleTourGroup(t *testing.T) {
	groups := GroupOutputs([]SubjectOutput{
		out(1, "html", "/o/my-slug/notes/page.html", 1),
		out(2, "html", "/o/my-slug/mockup/t3/index.html", 2),
	})
	if len(groups) != 2 {
		t.Fatalf("groups = %+v; want 2 (the page alone + its round)", groups)
	}
	g := groups[0]
	if g.Kind != "html" || g.Key != "/o/my-slug/notes/page.html" || len(g.Tours) != 1 || g.Tours[0].N != 0 || g.Tours[0].Label != "page.html" {
		t.Fatalf("standalone html = %+v", g)
	}
	r := groups[1]
	if r.Key != "/o/my-slug/mockup" || r.Name != "my-slug" || len(r.Tours) != 1 || r.Tours[0].N != 3 || r.Tours[0].Label != "t3" {
		t.Fatalf("html in a round = %+v", r)
	}
}
