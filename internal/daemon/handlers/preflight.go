package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// ghPRStatus checks the GitHub PR status for the given branch using the gh CLI.
// Returns nil gracefully if gh is not installed, not authenticated, or no PR found.
func ghPRStatus(ctx context.Context, wsPath, branch string) *pigtail.PreflightDelivery {
	//nolint:gosec // G702: branch is a local git branch name from workspace metadata, not arbitrary user input
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", branch,
		"--json", "url,state,statusCheckRollup",
		"--jq", `{url: .url, state: .state, ci: (if (.statusCheckRollup | length) == 0 then "none" elif (.statusCheckRollup | all(.conclusion == "SUCCESS")) then "success" elif (.statusCheckRollup | any(.conclusion == "FAILURE")) then "failure" else "pending" end)}`)
	cmd.Dir = wsPath
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var result struct {
		URL   string `json:"url"`
		State string `json:"state"`
		CI    string `json:"ci"`
	}
	if json.Unmarshal(out, &result) != nil {
		return nil
	}
	return &pigtail.PreflightDelivery{
		URL:      result.URL,
		State:    strings.ToLower(result.State),
		CIStatus: result.CI,
	}
}

// PreflightWorkspace handles GET /workspaces/{id}/preflight.
//
// Aggregates backend.Status + delivery lookup into a single response
// with a safe/warning verdict.
func PreflightWorkspace(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		ws, err := svc.Store.GetWorkspace(id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrWorkspaceNotFound, "no such workspace", map[string]interface{}{"id": id})
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}

		var branch string
		if svc.Backend != nil {
			branch = svc.Backend.DisplayRef(ws)
		}

		var gitInfo pigtail.PreflightGit
		if svc.Backend != nil {
			st, err := svc.Backend.Status(r.Context(), ws)
			if err != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrBackendError, "status: "+err.Error(), nil)
				return
			}
			gitInfo = pigtail.PreflightGit{
				Clean:         st.Clean,
				Ahead:         st.AheadOfBase,
				Behind:        st.BehindBase,
				Merged:        st.Merged,
				ModifiedFiles: st.ModifiedFiles,
				Commits:       st.RecentCommits,
			}
			if len(gitInfo.ModifiedFiles) > 5 {
				gitInfo.ModifiedFiles = gitInfo.ModifiedFiles[:5]
			}

			// Containment is an OPTIONAL backend capability (architecture.md
			// §9): a backend with no shared base to merge into simply doesn't
			// implement it, and preflight degrades to the narrow `Merged`
			// answer above rather than failing.
			if mi, ok := svc.Backend.(backend.MergeInspector); ok {
				ms, err := mi.MergeState(r.Context(), ws)
				if err != nil && r.Context().Err() != nil {
					resp.Error(w, r, http.StatusRequestTimeout, pigtail.ErrBackendError, "merge state: "+err.Error(), nil)
					return
				}
				gitInfo.MergeStatus = string(ms.Status)
				gitInfo.MergedHow = ms.How
				gitInfo.MergeBaseRef = ms.BaseRef
				gitInfo.MergeNote = ms.Note
			}
		}

		// GitHub PR lookup via gh CLI
		var delivery *pigtail.PreflightDelivery
		if branch != "" {
			delivery = ghPRStatus(r.Context(), ws.Path, branch)
		}

		verdict := "warning"
		if (gitInfo.Merged || gitInfo.MergeStatus == string(backend.MergeContained)) && gitInfo.Clean {
			verdict = "safe"
		} else if delivery != nil && delivery.State == "merged" && gitInfo.Clean {
			verdict = "safe"
		}

		resp.JSON(w, r, http.StatusOK, pigtail.PreflightResponse{
			WorkspaceID: ws.ID,
			Branch:      branch,
			Git:         gitInfo,
			Delivery:    delivery,
			Verdict:     verdict,
		})
	}
}
