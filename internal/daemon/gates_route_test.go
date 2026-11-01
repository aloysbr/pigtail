package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestGatesRouteRunsGateCmdBehindScope(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	dir := t.TempDir()
	cmd := filepath.Join(dir, "gate")
	body := `#!/bin/sh
[ "$1 $2" = "list --json" ] || { echo "unexpected args: $*" >&2; exit 2; }
cat <<'EOF'
[{"id":"t1","title":"Work gate","action":"a","scope":"gx","added":"2026-10-01T09:00:00+02:00","status":"fired","errors":0,"fired_at":"2026-10-02T09:00:00+02:00","subject_id":null,"value":"v","last_check":null},
 {"id":"p1","title":"Zqx perso gate","action":"a","scope":"perso","added":"2026-10-01T09:00:00+02:00","status":"open","errors":0,"fired_at":null,"subject_id":null,"value":"v","last_check":null}]
EOF
`
	if err := os.WriteFile(cmd, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open(":memory:")
	defer st.Close()
	srv, err := New(Config{SocketPath: testSocketPath(t), GateCmd: cmd}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	router := srv.buildRouter()
	get := func(hdr string) (string, pigtail.GatesResponse) {
		req := httptest.NewRequest(http.MethodGet, "/gates", nil)
		req.RemoteAddr = "" // the unix socket: no token needed
		if hdr != "" {
			req.Header.Set(pigtail.HeaderScope, hdr)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var env struct {
			Data pigtail.GatesResponse `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		return rec.Body.String(), env.Data
	}
	_, m := get(string(scope.Master()))
	if m.Counts == nil || m.Counts.Fired != 1 || m.Counts.Open != 1 || !m.Complete {
		t.Fatalf("master = %+v", m)
	}
	raw, work := get("")
	if strings.Contains(raw, "Zqx perso gate") || work.Complete {
		t.Fatalf("work caller read a perso gate: %s", raw)
	}
}
