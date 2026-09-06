package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// projectsHandler serves the shape the real endpoint returns, on the one path
// this feature depends on.
func projectsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/users/me/projects/" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"count":    2,
			"next":     nil,
			"previous": nil,
			"results": []map[string]interface{}{
				{
					"id":               "pCTMk",
					"isDefault":        true,
					"name":             "Project without a budget",
					"remainingAmounts": []string{"(no budget)"},
				},
				{
					"id":               "BNTMk",
					"isDefault":        false,
					"name":             "Project with a budget",
					"remainingAmounts": []string{"All: My budget ($100.00 available)"},
				},
			},
		})
	}
}

func TestListProjects(t *testing.T) {
	t.Run("decodes what the endpoint returns", func(t *testing.T) {
		server := httptest.NewServer(projectsHandler())
		defer server.Close()

		projects, err := newTestClient(t, server.URL).ListProjects(context.Background())
		if err != nil {
			t.Fatalf("ListProjects: %v", err)
		}

		if len(projects) != 2 {
			t.Fatalf("got %d projects, want 2", len(projects))
		}
		if projects[0].ID != "pCTMk" || projects[0].Name != "Project without a budget" || !projects[0].IsDefault {
			t.Errorf("first project = %+v", projects[0])
		}
		if projects[1].IsDefault {
			t.Errorf("second project is marked default: %+v", projects[1])
		}
		// The budget lines are what tell two same-named projects apart in the
		// picker, so they have to survive decoding.
		if len(projects[1].RemainingAmounts) != 1 ||
			!strings.Contains(projects[1].RemainingAmounts[0], "$100.00 available") {
			t.Errorf("remainingAmounts = %v, want the platform's budget line", projects[1].RemainingAmounts)
		}
	})

	// A refused request is an error, not an empty list: an empty picker reads as
	// "this account has no projects", which is a different thing entirely.
	t.Run("a refused request is an error", func(t *testing.T) {
		forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "no", http.StatusForbidden)
		}))
		defer forbidden.Close()

		if _, err := newTestClient(t, forbidden.URL).ListProjects(context.Background()); err == nil {
			t.Error("ListProjects succeeded on a 403")
		}
	})
}

// Pagination follows the same next-cursor convention as the other list
// endpoints, including a full URL that has to be reduced to an API path.
func TestListProjects_FollowsPagination(t *testing.T) {
	var server *httptest.Server
	seenPage2 := false

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/users/me/projects/" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Query().Get("page") == "2" {
			seenPage2 = true
			json.NewEncoder(w).Encode(map[string]interface{}{
				"count":   2,
				"next":    nil,
				"results": []map[string]interface{}{{"id": "second", "name": "Second"}},
			})
			return
		}

		next := server.URL + "/api/v2/users/me/projects/?page=2"
		json.NewEncoder(w).Encode(map[string]interface{}{
			"count":   2,
			"next":    next,
			"results": []map[string]interface{}{{"id": "first", "name": "First"}},
		})
	}))
	defer server.Close()

	projects, err := newTestClient(t, server.URL).ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if !seenPage2 {
		t.Fatal("second page was never requested")
	}
	if len(projects) != 2 || projects[1].ID != "second" {
		t.Errorf("got %+v, want both pages", projects)
	}
}

// The org code is a property of the API key, so it is resolved from the key's
// own profile rather than asked of the user.
func TestOrgCode_ResolvesFromProfileAndCaches(t *testing.T) {
	t.Run("resolved once, however many callers ask", func(t *testing.T) {
		var mu sync.Mutex
		profileCalls := 0

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v3/users/me/" {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			mu.Lock()
			profileCalls++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"email":   "someone@example.com",
				"company": map[string]string{"code": "acme"},
			})
		}))
		defer server.Close()

		client := newTestClient(t, server.URL)

		// Concurrent callers, because the pipeline assigns projects from several
		// job workers at once and none should trigger its own profile fetch.
		var wg sync.WaitGroup
		codes := make([]string, 5)
		for i := range codes {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				code, err := client.OrgCode(context.Background())
				if err != nil {
					t.Errorf("OrgCode: %v", err)
					return
				}
				codes[i] = code
			}(i)
		}
		wg.Wait()

		for i, code := range codes {
			if code != "acme" {
				t.Errorf("caller %d got org code %q, want acme", i, code)
			}
		}

		mu.Lock()
		if profileCalls != 1 {
			t.Errorf("profile fetched %d times, want 1 — the code cannot change for a key", profileCalls)
		}
		mu.Unlock()
	})

	// A profile with no company code cannot address an org-scoped endpoint, so
	// it fails rather than building a URL with an empty organization in it.
	t.Run("a profile with no company code", func(t *testing.T) {
		bare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"email": "someone@example.com"})
		}))
		defer bare.Close()

		if _, err := newTestClient(t, bare.URL).OrgCode(context.Background()); err == nil {
			t.Error("OrgCode succeeded on a profile with no company code")
		} else if !errors.Is(err, ErrOrgCodeUnavailable) {
			t.Errorf("OrgCode error %q is not ErrOrgCodeUnavailable, so an assignment would retry it", err)
		}
	})
}

// Choosing a project is enough on its own: the assignment path fills in the org
// code the user used to have to type. An explicit code is an override, used
// verbatim and costing no profile request.
func TestAssignProjectToJob_OrgCode(t *testing.T) {
	assignedPath, profileCalls := "", 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/users/me/":
			profileCalls++
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"company": map[string]string{"code": "acme"},
			})
		case strings.HasSuffix(r.URL.Path, "/project-assignment/"):
			assignedPath = r.URL.Path
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["projectId"] != "pCTMk" {
				t.Errorf("projectId = %q, want pCTMk", body["projectId"])
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	tests := []struct {
		name             string
		orgCode          string
		wantPath         string
		wantProfileCalls int
	}{
		{"omitted, resolved from the profile", "", "/api/v2/organizations/acme/jobs/job123/project-assignment/", 1},
		{"explicit, used verbatim", "other", "/api/v2/organizations/other/jobs/job123/project-assignment/", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A fresh client per case, since OrgCode caches what it resolved.
			assignedPath, profileCalls = "", 0
			client := newTestClient(t, server.URL)

			if err := client.AssignProjectToJob(context.Background(), tt.orgCode, "job123", "pCTMk"); err != nil {
				t.Fatalf("AssignProjectToJob: %v", err)
			}
			if assignedPath != tt.wantPath {
				t.Errorf("assignment path = %q, want %q", assignedPath, tt.wantPath)
			}
			if profileCalls != tt.wantProfileCalls {
				t.Errorf("profile fetched %d times, want %d", profileCalls, tt.wantProfileCalls)
			}
		})
	}
}
