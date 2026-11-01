package handlers

import (
	"html/template"
	"net/http"
	"net/url"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// boardColumns is the kanban column order shown on the board: the status
// vocabulary in its order (D1), without captured (a capture now, hidden).
var boardColumns = func() []string {
	out := make([]string, 0, len(pigtail.SubjectStatuses))
	for _, s := range pigtail.SubjectStatuses {
		if s != pigtail.StatusCaptured {
			out = append(out, s)
		}
	}
	return out
}()

// boardCard is one subject as rendered on the board. ObsidianURL is typed
// template.URL so html/template does not sanitise the non-http "obsidian://"
// scheme to "#ZgotmplZ"; the dynamic path is QueryEscape'd before wrapping, so
// this is safe.
type boardCard struct {
	pigtail.Subject
	ThemeName   string
	ObsidianURL template.URL
}

type boardColumn struct {
	Status string
	Cards  []boardCard
}

type boardData struct {
	Columns []boardColumn
	Count   int
	Scope   string // a scope name, or all
}

var boardTmpl = template.Must(template.New("board").Parse(boardHTML))

// boardScope reads the board's ?scope= query. The board is public — a
// browser cannot send the scope header — so the query picks the view, and its
// absence is the default scope: a tab opened without thinking never shows the
// master's. The board is the master's view (D20, decided by the user,
// 2026-10-05): a localhost surface reserved to the user, a stated exception to
// D1/D19 — every explicit choice is allowed, all included. Scope isolation
// guards against an agent's mistake, not against an agent looking for another
// org. Its 400 echoes the name it got and lists no scope.
func boardScope(q string) (sc scope.Scope, label string, err error) {
	if q == "" {
		d := scope.Default()
		return d, string(d), nil
	}
	sc, err = scope.ListFilter(scope.Master(), q)
	if err != nil {
		return "", "", scope.NotDeclared(q) // the master reads every scope: only an unknown name fails
	}
	if sc == scope.Any {
		return sc, "all", nil
	}
	return sc, string(sc), nil
}

// Board handles GET /board?scope=<name>|all: a read-only kanban of
// subjects from the store, the default scope by default.
func Board(svc *Services) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sc, label, err := boardScope(r.URL.Query().Get("scope"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		themes, err := svc.Store.ListThemes(sc)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		themeName := make(map[string]string, len(themes))
		for _, th := range themes {
			themeName[th.ID] = th.Name
		}
		subs, err := svc.Store.ListSubjects(store.SubjectFilter{Scope: sc})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		byStatus := make(map[string][]boardCard)
		shown := 0
		for _, s := range subs {
			if s.Status == pigtail.StatusCaptured {
				continue // converted into a capture (D2): not on the board
			}
			shown++
			card := boardCard{Subject: s, ThemeName: themeName[s.ThemeID]}
			if s.VaultPath != "" {
				// The dynamic path is QueryEscape'd; template.URL only preserves the
				// trusted "obsidian://" scheme (html/template would otherwise rewrite it
				// to "#ZgotmplZ"). Safe — no untrusted markup reaches the attribute.
				card.ObsidianURL = template.URL("obsidian://open?path=" + url.QueryEscape(s.VaultPath)) //nolint:gosec // G203: path is QueryEscape'd; trusted obsidian:// scheme intentionally preserved
			}
			byStatus[s.Status] = append(byStatus[s.Status], card)
		}
		data := boardData{Count: shown, Scope: label}
		for _, col := range boardColumns {
			data.Columns = append(data.Columns, boardColumn{Status: col, Cards: byStatus[col]})
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := boardTmpl.Execute(w, data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

const boardHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>pigtail · subjects · {{.Scope}}</title>
<style>
 body{font:14px/1.4 -apple-system,system-ui,sans-serif;margin:0;background:#0f1115;color:#e6e6e6}
 header{padding:16px 20px;border-bottom:1px solid #222;font-weight:600}
 .board{display:flex;gap:12px;padding:16px;overflow-x:auto}
 .col{flex:0 0 240px;background:#161922;border:1px solid #222;border-radius:10px;padding:10px}
 .col h2{font-size:11px;text-transform:uppercase;letter-spacing:.06em;color:#8a93a6;margin:2px 4px 10px}
 .card{background:#1d2230;border:1px solid #2a3142;border-radius:8px;padding:10px;margin-bottom:8px}
 .card .title{font-weight:600;margin-bottom:6px}
 .meta{font-size:11px;color:#8a93a6}
 .theme{display:inline-block;font-size:10px;color:#9fb4ff;margin-bottom:4px}
 .deps{font-size:10px;color:#e0a35a;margin-top:4px}
 a{color:#7aa2ff;text-decoration:none}
 .empty{color:#55607a;font-size:12px;padding:4px}
</style></head><body>
<header>pigtail · subjects · {{.Scope}} ({{.Count}})</header>
<div class="board">
{{range .Columns}}
 <div class="col"><h2>{{.Status}} ({{len .Cards}})</h2>
 {{range .Cards}}
  <div class="card">
   {{if .ThemeName}}<span class="theme">{{.ThemeName}}</span><br>{{end}}
   <div class="title">{{if .ObsidianURL}}<a href="{{.ObsidianURL}}">{{.Title}}</a>{{else}}{{.Title}}{{end}}</div>
   <div class="meta">rank {{.Rank}} · impact {{.Impact}} · effort {{.Effort}} · urgency {{.Urgency}}</div>
   {{if .Deps}}<div class="deps">blocked by: {{range .Deps}}{{.}} {{end}}</div>{{end}}
  </div>
 {{else}}<div class="empty">—</div>{{end}}
 </div>
{{end}}
</div></body></html>`
