package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// projectsHandler serves the shape the real endpoint returns, on the one path
// this feature depends on.
func projectsHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
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
	server := httptest.NewServer(projectsHandler(t))
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
	// The budget lines are what tell two same-named projects apart in the picker,
	// so they have to survive decoding.
	if len(projects[1].RemainingAmounts) != 1 ||
		!strings.Contains(projects[1].RemainingAmounts[0], "$100.00 available") {
		t.Errorf("remainingAmounts = %v, want the platform's budget line", projects[1].RemainingAmounts)
	}
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

func TestListProjects_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer server.Close()

	if _, err := newTestClient(t, server.URL).ListProjects(context.Background()); err == nil {
		t.Fatal("ListProjects succeeded on a 403")
	}
}

// The org code is a property of the API key, so it is resolved from the key's
// own profile rather than asked of the user.
func TestOrgCode_ResolvesFromProfileAndCaches(t *testing.T) {
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

	// Concurrent callers, because the pipeline assigns projects from several job
	// workers at once and none of them should trigger its own profile fetch.
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
	defer mu.Unlock()
	if profileCalls != 1 {
		t.Errorf("profile fetched %d times, want 1 — the code cannot change for a key", profileCalls)
	}
}

func TestOrgCode_ProfileWithoutCompanyCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"email": "someone@example.com"})
	}))
	defer server.Close()

	if _, err := newTestClient(t, server.URL).OrgCode(context.Background()); err == nil {
		t.Fatal("OrgCode succeeded on a profile with no company code")
	}
}

// Choosing a project is enough on its own: the assignment path fills in the org
// code the user used to have to type.
func TestAssignProjectToJob_ResolvesOrgCodeWhenOmitted(t *testing.T) {
	assignedPath := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/users/me/":
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

	if err := newTestClient(t, server.URL).AssignProjectToJob(context.Background(), "", "job123", "pCTMk"); err != nil {
		t.Fatalf("AssignProjectToJob: %v", err)
	}

	want := "/api/v2/organizations/acme/jobs/job123/project-assignment/"
	if assignedPath != want {
		t.Errorf("assignment path = %q, want %q", assignedPath, want)
	}
}

// An explicit code is an override, so it must be used verbatim and cost no
// profile request.
func TestAssignProjectToJob_ExplicitOrgCodeWins(t *testing.T) {
	assignedPath := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/users/me/" {
			t.Error("profile fetched despite an explicit org code")
			http.Error(w, "unexpected", http.StatusInternalServerError)
			return
		}
		assignedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := newTestClient(t, server.URL).AssignProjectToJob(context.Background(), "other", "job123", "pCTMk"); err != nil {
		t.Fatalf("AssignProjectToJob: %v", err)
	}

	want := "/api/v2/organizations/other/jobs/job123/project-assignment/"
	if assignedPath != want {
		t.Errorf("assignment path = %q, want %q", assignedPath, want)
	}
}
