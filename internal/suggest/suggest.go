// Package suggest ranks the subjects a pane's request may belong to, from
// three signals: a PR the subject's own log names, the pane's worktree or
// branch, and the words of the subject's title. It is pure: no store, no HTTP,
// no git — the daemon reads, this package judges.
package suggest

import (
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Subject is one subject of the corpus, every scope and status included.
type Subject struct{ ID, ShortTitle, Title, Scope, Status string }

// LogLine is one subject_log entry.
type LogLine struct{ SubjectID, Kind, Text string }

// Linked is a worktree bound to a subject through subject_phases.workspace_id.
type Linked struct{ SubjectID, Path string }

// Query is what the pane brings.
type Query struct {
	Text          string // request (+ off-topic reason), already cut to 4096 bytes by the caller
	Cwd           string // cleaned absolute path, "" = unknown
	Branch        string // the pane's branch, "" = unknown
	DefaultBranch string // the pane repo's default branch, "" = unknown
	Repo          string // last segment of remote.origin.url without .git, "" = unknown
	Limit         int    // 1..10, default 3 (applied by the caller: Rank never truncates)
}

// Candidate is one ranked subject.
type Candidate struct {
	SubjectID, ShortTitle, Title, Scope, Status string
	Signal   string // "pr" | "worktree" | "keyword"
	Strength string // "strong" | "weak"
	Evidence string // internal (JSON), never in the announcement
	PR       int    // the PR number of the strong pr signal, 0 otherwise
	Score    float64
}

// PRRef is a pull request reference; Repo "" is a bare number.
type PRRef struct {
	Repo   string
	Number int
}

const (
	SignalPR       = "pr"
	SignalWorktree = "worktree"
	SignalKeyword  = "keyword"
	Strong         = "strong"
	Weak           = "weak"

	// KeywordMinScore is the idf sum a subject needs to be a keyword candidate.
	KeywordMinScore = 1.5
)

var (
	urlPRRE  = regexp.MustCompile(`github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/pull/([0-9]+)`)
	hashPRRE = regexp.MustCompile(`([A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)?)?#([0-9]+)`)
	wordPRRE = regexp.MustCompile(`(?i)\bPR(?:\s+#?|#)([0-9]+)(?:\s*/\s*#?([0-9]+))?`)
	branchRE = regexp.MustCompile(`(?:^|[^A-Za-z0-9_/])((?:feat|fix|chore|refactor|perf|hotfix|release)/[A-Za-z0-9._-]+)`)
)

// prNumber parses a 2-7 digit PR number; the regexes match the whole digit
// run, so the right bound (end or non-digit) holds by construction.
func prNumber(s string) (int, bool) {
	if len(s) < 2 || len(s) > 7 {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

func isAlnum(r byte) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

type posRef struct {
	pos int
	ref PRRef
}

// ExtractPRRefs returns the PR references of text, in order, without
// duplicates: owner/repo#n, repo#n, #n, PR n, PR #n, PR n/m and GitHub pull
// URLs. A number glued to a letter or a digit on its left is not one.
func ExtractPRRefs(text string) []PRRef {
	var found []posRef
	for _, m := range urlPRRE.FindAllStringSubmatchIndex(text, -1) {
		if n, ok := prNumber(text[m[4]:m[5]]); ok {
			found = append(found, posRef{m[0], PRRef{text[m[2]:m[3]], n}})
		}
	}
	for _, m := range hashPRRE.FindAllStringSubmatchIndex(text, -1) {
		start := m[0]
		if start > 0 && (isAlnum(text[start-1]) || text[start-1] == '/') {
			continue // glued to a word or inside a path
		}
		n, ok := prNumber(text[m[4]:m[5]])
		if !ok {
			continue
		}
		repo := ""
		if m[2] >= 0 {
			repo = text[m[2]:m[3]]
		}
		found = append(found, posRef{start, PRRef{repo, n}})
	}
	for _, m := range wordPRRE.FindAllStringSubmatchIndex(text, -1) {
		if n, ok := prNumber(text[m[2]:m[3]]); ok {
			found = append(found, posRef{m[2], PRRef{"", n}})
		}
		if m[4] >= 0 {
			if n, ok := prNumber(text[m[4]:m[5]]); ok {
				found = append(found, posRef{m[4], PRRef{"", n}})
			}
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].pos < found[j].pos })
	var out []PRRef
	seen := map[PRRef]bool{}
	for _, f := range found {
		if !seen[f.ref] {
			seen[f.ref] = true
			out = append(out, f.ref)
		}
	}
	return out
}

func lastSegment(repo string) string {
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		repo = repo[i+1:]
	}
	return strings.ToLower(repo)
}

// refMatches says whether a reference found in a log line names the queried one.
func refMatches(q, line PRRef) bool {
	if q.Number != line.Number {
		return false
	}
	return q.Repo == "" || line.Repo == "" || lastSegment(q.Repo) == lastSegment(line.Repo)
}

// lineRef returns the first reference of text naming q.
func lineRef(q PRRef, text string) (PRRef, bool) {
	for _, r := range ExtractPRRefs(text) {
		if refMatches(q, r) {
			return r, true
		}
	}
	return PRRef{}, false
}

func evidenceOf(r PRRef) string {
	if r.Repo != "" {
		return r.Repo + "#" + strconv.Itoa(r.Number)
	}
	return "PR #" + strconv.Itoa(r.Number)
}

// hasBranch says whether s names a branch (feat/…, fix/…, …) not followed by
// a path separator: build/polish (a track) and docs/… (a path) are not ones.
func hasBranch(s string) bool {
	for _, m := range branchRE.FindAllStringSubmatchIndex(s, -1) {
		if end := m[3]; end < len(s) && s[end] == '/' {
			continue
		}
		return true
	}
	return false
}

// ownerKinds are the kinds a subject writes about its own state.
var ownerKinds = map[string]bool{"delivery": true, "phase": true, "progress": true, "handoff": true}

// prTier is the ownership tier of a log entry for q: 1 delivery, 2 phase or
// handoff naming q and a branch on one text line, 3 other owner lines, 4
// note/decision; 0 = it does not name q or its kind never counts.
func prTier(q PRRef, l LogLine) (int, PRRef) {
	var base int
	switch l.Kind {
	case "delivery":
		base = 1
	case "phase", "handoff":
		base = 3
	case "progress":
		base = 3
	case "note", "decision":
		base = 4
	default:
		return 0, PRRef{}
	}
	ref, ok := lineRef(q, l.Text)
	if !ok {
		return 0, PRRef{}
	}
	if l.Kind == "phase" || l.Kind == "handoff" {
		for _, line := range strings.Split(l.Text, "\n") {
			if r, ok := lineRef(q, line); ok && hasBranch(line) {
				return 2, r
			}
		}
	}
	return base, ref
}

// defaultBranches never prove a worktree.
var defaultBranches = map[string]bool{"main": true, "master": true, "develop": true, "development": true, "trunk": true, "HEAD": true}

// IsDefaultBranch says whether branch is the pane repo's default branch or one
// of the fixed default names.
func IsDefaultBranch(branch, defaultBranch string) bool {
	return defaultBranches[branch] || (defaultBranch != "" && branch == defaultBranch)
}

func isBranchChar(r byte) bool {
	return isAlnum(r) || r == '_' || r == '/' || r == '-' || r == '.'
}

// namesWholeWord says whether text names branch as a whole word: no branch
// character glued on either side (a trailing sentence dot is not glued).
func namesWholeWord(text, branch string) bool {
	for off := 0; ; {
		i := strings.Index(text[off:], branch)
		if i < 0 {
			return false
		}
		start, end := off+i, off+i+len(branch)
		before := start == 0 || !isBranchChar(text[start-1])
		after := end == len(text) || !isBranchChar(text[end]) ||
			(text[end] == '.' && (end+1 == len(text) || !isBranchChar(text[end+1])))
		if before && after {
			return true
		}
		off = start + 1
	}
}

func under(path, root string) bool {
	if path == "" || root == "" {
		return false
	}
	path, root = filepath.Clean(path), filepath.Clean(root)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel)
}

// hit is one signal found for one subject.
type hit struct {
	signal, strength, evidence string
	pr                         int
}

func signalRank(signal string) int {
	switch signal {
	case SignalPR:
		return 0
	case SignalWorktree:
		return 1
	}
	return 2
}

func hitLess(a, b hit) bool {
	if a.strength != b.strength {
		return a.strength == Strong
	}
	return signalRank(a.signal) < signalRank(b.signal)
}

// idf is the smoothed inverse document frequency: ln((N+1)/(df+1)).
func idf(n, df int) float64 {
	return math.Log(float64(n+1) / float64(df+1))
}

// Rank returns every candidate of every scope, merged one per subject and
// sorted: strong first (pr then worktree), then weak (pr then keyword), then
// score, then id. It never truncates.
func Rank(q Query, subjects []Subject, logs []LogLine, linked []Linked) []Candidate {
	byID := make(map[string]Subject, len(subjects))
	for _, s := range subjects {
		byID[s.ID] = s
	}
	live := func(id string) bool {
		s, ok := byID[id]
		// D26 t3: a PR reference names its delivered subject, so done and
		// parked stay offered here; only dropped and captured are out.
		return ok && s.Status != pigtail.StatusDropped && s.Status != pigtail.StatusCaptured
	}
	hits := map[string][]hit{}
	add := func(id string, h hit) { hits[id] = append(hits[id], h) }

	// PR signal: per reference, the highest tier present decides alone.
	for _, ref := range ExtractPRRefs(q.Text) {
		best := 5
		type owned struct {
			tier int
			ref  PRRef
		}
		var lines []struct {
			id string
			owned
		}
		for _, l := range logs {
			if !live(l.SubjectID) {
				continue
			}
			t, r := prTier(ref, l)
			if t == 0 {
				continue
			}
			lines = append(lines, struct {
				id string
				owned
			}{l.SubjectID, owned{t, r}})
			if t < best {
				best = t
			}
		}
		shortBare := ref.Repo == "" && ref.Number < 1000
		owners := map[string]PRRef{}
		var order []string
		for _, l := range lines {
			if l.tier != best {
				continue
			}
			prev, seen := owners[l.id]
			if !seen {
				order = append(order, l.id)
				owners[l.id] = l.ref
				continue
			}
			// A short bare reference prefers the line naming the pane's repo.
			if shortBare && !repoIs(prev, q.Repo) && repoIs(l.ref, q.Repo) {
				owners[l.id] = l.ref
			}
		}
		for _, id := range order {
			r := owners[id]
			h := hit{signal: SignalPR, strength: Strong, evidence: evidenceOf(r), pr: ref.Number}
			if shortBare && !repoIs(r, q.Repo) {
				h.strength, h.pr = Weak, 0
			}
			add(id, h)
		}
	}

	// Worktree signal: the pane sits in a linked worktree, or its branch is
	// named by the subject's own lines.
	if q.Cwd != "" {
		for _, l := range linked {
			if live(l.SubjectID) && under(q.Cwd, l.Path) {
				add(l.SubjectID, hit{signal: SignalWorktree, strength: Strong, evidence: "dossier de travail lié"})
			}
		}
	}
	if q.Branch != "" && !IsDefaultBranch(q.Branch, q.DefaultBranch) {
		for _, l := range logs {
			if live(l.SubjectID) && ownerKinds[l.Kind] && namesWholeWord(l.Text, q.Branch) {
				add(l.SubjectID, hit{signal: SignalWorktree, strength: Strong, evidence: "branche " + q.Branch})
			}
		}
	}

	// Keyword signal: idf over the titles of every subject passed in.
	scores := map[string]float64{}
	if qt := Tokens(q.Text); len(qt) > 0 && len(subjects) > 0 {
		docs := make(map[string]map[string]bool, len(subjects))
		df := map[string]int{}
		for _, s := range subjects {
			set := map[string]bool{}
			for _, t := range Tokens(s.Title + " " + s.ShortTitle) {
				set[t] = true
			}
			docs[s.ID] = set
			for t := range set {
				df[t]++
			}
		}
		for _, s := range subjects {
			if pigtail.IsClosedStatus(s.Status) {
				continue
			}
			var score float64
			var words []string
			for _, t := range qt {
				if docs[s.ID][t] {
					score += idf(len(subjects), df[t])
					words = append(words, t)
				}
			}
			if score >= KeywordMinScore {
				scores[s.ID] = score
				add(s.ID, hit{signal: SignalKeyword, strength: Weak, evidence: "mots : " + strings.Join(words, ", ")})
			}
		}
	}

	out := make([]Candidate, 0, len(hits))
	for id, hs := range hits {
		sort.SliceStable(hs, func(i, j int) bool { return hitLess(hs[i], hs[j]) })
		s := byID[id]
		c := Candidate{
			SubjectID: id, ShortTitle: s.ShortTitle, Title: s.Title, Scope: s.Scope, Status: s.Status,
			Signal: hs[0].signal, Strength: hs[0].strength, Score: scores[id],
		}
		var ev []string
		seen := map[string]bool{}
		for _, h := range hs {
			if !seen[h.evidence] {
				seen[h.evidence] = true
				ev = append(ev, h.evidence)
			}
			if c.PR == 0 && h.signal == SignalPR && h.strength == Strong {
				c.PR = h.pr
			}
		}
		c.Evidence = strings.Join(ev, ", ")
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		ha, hb := hit{signal: a.Signal, strength: a.Strength}, hit{signal: b.Signal, strength: b.Strength}
		if hitLess(ha, hb) != hitLess(hb, ha) {
			return hitLess(ha, hb)
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return a.SubjectID < b.SubjectID
	})
	return out
}

func repoIs(r PRRef, repo string) bool {
	return repo != "" && r.Repo != "" && lastSegment(r.Repo) == strings.ToLower(repo)
}

// accents folds the French (and a few neighbouring) accented letters.
var accents = strings.NewReplacer(
	"à", "a", "â", "a", "ä", "a", "á", "a", "ã", "a", "å", "a",
	"ç", "c",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"î", "i", "ï", "i", "í", "i", "ì", "i",
	"ô", "o", "ö", "o", "ó", "o", "ò", "o", "õ", "o",
	"ù", "u", "û", "u", "ü", "u", "ú", "u",
	"ÿ", "y", "ñ", "n", "œ", "oe", "æ", "ae",
)

var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`
		les des une dans pour par sur avec sans sous chez entre vers est sont etre ete avoir fait faire
		qui que quoi dont ou mais donc car pas plus moins tres tout tous toute toutes cette ces cet son
		sa ses leur leurs nous vous ils elles elle lui aux du de la le un et en au ce se ne on il je tu
		mon ma mes ton ta tes notre votre nos vos meme aussi comme alors encore deja ici quand comment
		pourquoi apres avant puis peut doit faut via ceci cela ca oui non bien
		the and for with without from into onto about this that these those are was were been being
		have has had does did doing not but nor any all some can could should would will shall may
		might must its our your their them they there then than when what which who whom why how
		also just only very more most such each other over under again here off out own same too
		yes still yet get got use using via per
	`) {
		m[w] = true
	}
	return m
}()

var decisionRE = regexp.MustCompile(`^d[0-9]+$`)

// Tokens returns the distinct meaningful words of text, in order: lower case,
// accents folded, split on anything but letters, digits and an inner hyphen,
// stop words, words under 3 letters, decision numbers (d12) and pure numbers
// removed.
func Tokens(text string) []string {
	text = accents.Replace(strings.ToLower(text))
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r != '-' && !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var out []string
	seen := map[string]bool{}
	for _, f := range fields {
		f = strings.Trim(f, "-")
		if len([]rune(f)) < 3 || stopwords[f] || decisionRE.MatchString(f) || seen[f] {
			continue
		}
		if strings.IndexFunc(f, func(r rune) bool { return !unicode.IsDigit(r) }) < 0 {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}
