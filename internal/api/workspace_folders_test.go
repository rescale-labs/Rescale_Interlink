package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/constants"
)

// A folder's jobs are listed for every owner, newest first, and a "next" link
// is followed on the configured host whatever host it names.
func TestListJobsInFolder_FollowsNextOnTheConfiguredHost(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/jobs/" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			json.NewEncoder(w).Encode(map[string]any{"next": nil, "results": []map[string]any{
				{"id": "job2", "folder": map[string]string{"id": "sub", "name": "Sub", "parentId": "root"}},
			}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"next":    "https://OTHER-HOST.example.invalid/api/v3/jobs/?page=2",
			"results": []map[string]any{{"id": "job1", "folder": map[string]string{"id": "root"}}},
		})
	}))
	defer server.Close()

	jobs, err := newTestClient(t, server.URL).ListJobsInFolder(context.Background(), "root", time.Time{})
	if err != nil {
		t.Fatalf("ListJobsInFolder: %v", err)
	}
	if len(jobs) != 2 || jobs[1].ID != "job2" || jobs[1].Folder == nil || jobs[1].Folder.ID != "sub" || jobs[1].Folder.ParentID != "root" {
		t.Errorf("jobs = %+v, want job1 and job2 with job2's folder", jobs)
	}
	if len(queries) != 2 || queries[0] != "q=folder:root&f=0&ordering=-dateInserted" || queries[1] != "page=2" {
		t.Errorf("queries = %q, want the folder's jobs for every owner, newest first, then page 2 here", queries)
	}
}

// A listing that runs past the page cap has not been listed: a partial one
// would read as complete.
func TestListJobsInFolder_PageLimitIsAnError(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"next":    "/api/v3/jobs/?page=next",
			"results": []map[string]any{{"id": "one-of-very-many"}},
		})
	}))
	defer server.Close()

	jobs, err := newTestClient(t, server.URL).ListJobsInFolder(context.Background(), "root", time.Time{})
	if err == nil || !strings.Contains(err.Error(), "listing incomplete") || jobs != nil {
		t.Fatalf("ListJobsInFolder = %d jobs, %v after %d pages; want no jobs and an incomplete-listing error", len(jobs), err, pages)
	}
	if pages != constants.MaxPaginationPages {
		t.Errorf("fetched %d pages, want the %d-page cap", pages, constants.MaxPaginationPages)
	}
}

// The listing stops at the first page whose jobs all predate the cutoff; a
// page with any newer job is followed by the next.
func TestListJobsInFolder_StopsAtTheCutoff(t *testing.T) {
	cutoff := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	before, after := cutoff.Add(-time.Hour).Format(time.RFC3339), cutoff.Add(time.Hour).Format(time.RFC3339)
	pages := map[string][]map[string]any{
		"":  {{"id": "new", "dateInserted": after}, {"id": "mixed", "dateInserted": before}},
		"2": {{"id": "old1", "dateInserted": before}, {"id": "old2", "dateInserted": before}},
		"3": {{"id": "never", "dateInserted": after}},
	}
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		requested = append(requested, page)
		next := map[string]string{"": "/api/v3/jobs/?page=2", "2": "/api/v3/jobs/?page=3"}[page]
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"next": next, "results": pages[page]})
	}))
	defer server.Close()

	jobs, err := newTestClient(t, server.URL).ListJobsInFolder(context.Background(), "root", cutoff)
	if err != nil {
		t.Fatalf("ListJobsInFolder: %v", err)
	}
	var ids []string
	for _, j := range jobs {
		ids = append(ids, j.ID)
	}
	if strings.Join(ids, ",") != "new,mixed,old1,old2" || strings.Join(requested, ",") != ",2" {
		t.Errorf("jobs %v from pages %q, want the first two pages' jobs and no third page", ids, requested)
	}
}

// The folder tree decodes with its nesting and archived flags.
func TestGetMetaFolders_DecodesTheTree(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/meta/folders/" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"sharedWithWorkspace": {"id": "root", "name": "Shared", "children": [
			{"id": "a", "name": "A", "children": [{"id": "b", "name": "B", "isArchived": true}]}]}}`))
	}))
	defer server.Close()

	meta, err := newTestClient(t, server.URL).GetMetaFolders(context.Background())
	if err != nil {
		t.Fatalf("GetMetaFolders: %v", err)
	}
	root := meta.SharedWithWorkspace
	if root.ID != "root" || len(root.Children) != 1 || len(root.Children[0].Children) != 1 || !root.Children[0].Children[0].IsArchived {
		t.Errorf("tree = %+v, want root > A > archived B", root)
	}
}
