package daemon

import (
	"cmp"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/models"
)

// fakeJob is a completed job on the fake platform: one of the user's own when
// it is in no workspace folder, and under the workspace's shared root when it
// is.
type fakeJob struct {
	id, name  string            // name: the ID when empty
	folder    string            // the workspace folder it is in, or ""
	created   time.Time         // zero: its listing gives no creation time
	completed time.Time         // when its Completed status is dated; zero: now
	fields    map[string]string // its custom fields; nil: Auto Download is Enabled
	files     []models.JobFile
	tags      []string
}

// platform serves what the daemon asks the Rescale API for, for its jobs and
// the workspace folder tree, to any number of clients, each through a server
// of its own. It keeps a job's tags as the platform does: a tag is added once
// and removed by name. Its fields are read under mu, so a test may change them
// between polls.
type platform struct {
	t *testing.T

	mu   sync.Mutex
	jobs []*fakeJob
	tree any // the workspace folder tree; nil: it is refused

	failLookups   bool                     // completion times cannot be read
	failListings  bool                     // job files cannot be listed
	failReadsFrom int                      // a client's tag reads from this one on fail; 0: none do
	failDeletes   int                      // tag removals to refuse before accepting them
	hangDeletes   bool                     // tag removals are never answered
	refuseClaims  bool                     // started tags are refused
	claimDelay    map[string]time.Duration // how long a client's started tag takes to put on
	listDelay     time.Duration            // how long listing a job's files takes

	onLookup   func(request context.Context) // runs as a completion time is looked up
	beforeRead func(client string, n int)    // runs before a client's nth tag read is answered
	onClaim    func(tag string)              // runs, mu held, once a started tag is on
	onFetch    func()                        // runs as a file's download starts, which then lasts until the client gives up; nil: downloads fail

	lookups    []string       // the jobs whose completion times were looked up, in order
	tagReads   []string       // the jobs whose tags were read, in order
	readsBy    map[string]int // tag reads, by client
	tagWrites  []string       // "+tag" for a tag put on, "-tag" for one taken off, in order
	fieldReads int            // reads of a job's custom fields
	listings   map[string]int // file listings, a download's first step, by client
}

func newPlatform(t *testing.T, jobs ...*fakeJob) *platform {
	return &platform{t: t, jobs: jobs, readsBy: map[string]int{}, listings: map[string]int{}}
}

// url starts a server through which client reaches the platform.
func (p *platform) url(client string) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { p.serve(client, w, r) }))
	p.t.Cleanup(srv.Close)
	return srv.URL
}

// daemon returns a daemon that reaches the platform and downloads into dir.
func (p *platform) daemon(dir string, elig EligibilityConfig) *Daemon {
	return newDownloadTestDaemon(p.t, p.url(""), dir, elig)
}

// monitor returns a monitor, with state, that reaches the platform.
func (p *platform) monitor(state *State, elig EligibilityConfig) *Monitor {
	client := api.NewClientForTest(&config.Config{APIKey: "test-key", APIBaseURL: p.url(""), ProxyMode: "no-proxy"})
	return NewMonitor(client, state, nil, elig, logging.NewLoggerWithWriter(io.Discard))
}

// client returns a daemon that reaches the platform through a server of its
// own, as client name, and downloads into a folder of its own, and a function
// returning what it has logged.
func (p *platform) client(name string, elig EligibilityConfig) (*Daemon, func() []string) {
	d := newDownloadTestDaemon(p.t, p.url(name), p.t.TempDir(), elig)
	var mu sync.Mutex
	var lines []string
	d.logger = logging.NewLoggerWithWriter(logHook(func(line string) {
		mu.Lock()
		lines = append(lines, line)
		mu.Unlock()
	}))
	return d, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(lines)
	}
}

func (p *platform) serve(client string, w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	id, what, _ := strings.Cut(strings.TrimPrefix(path, "/api/v3/jobs/"), "/")
	var body any
	switch {
	case path == "/api/v3/jobs/":
		body = p.listing(r.URL.Query().Get("q") == "folder:root")
	case path == "/api/v3/meta/folders/":
		p.mu.Lock()
		body = p.tree
		p.mu.Unlock()
		if body == nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
	case what == "statuses/":
		p.mu.Lock()
		p.lookups = append(p.lookups, id)
		j, fail, onLookup, at := p.job(id), p.failLookups, p.onLookup, time.Now()
		if j != nil && !j.completed.IsZero() {
			at = j.completed
		}
		p.mu.Unlock()
		if onLookup != nil {
			onLookup(r.Context())
		}
		if fail || j == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body = map[string]any{"results": []models.JobStatusEntry{{Status: "Completed", StatusDate: at.UTC().Format(time.RFC3339)}}}
	case what == "tags/":
		p.serveTags(client, id, w, r)
		return
	case what == "custom-fields/":
		fields := map[string]any{}
		p.mu.Lock()
		p.fieldReads++
		if j := p.job(id); j != nil && j.fields == nil {
			fields[config.AutoDownloadFieldName] = map[string]any{"value": "Enabled"}
		} else if j != nil {
			for name, value := range j.fields {
				fields[name] = map[string]any{"value": value}
			}
		}
		p.mu.Unlock()
		body = fields
	case strings.HasPrefix(path, "/api/v2/jobs/"):
		p.mu.Lock()
		p.listings[client]++
		j, fail, delay := p.job(strings.TrimSuffix(strings.TrimPrefix(path, "/api/v2/jobs/"), "/files/")), p.failListings, p.listDelay
		var files []models.JobFile
		if j != nil {
			files = slices.Clone(j.files)
		}
		p.mu.Unlock()
		time.Sleep(delay)
		if fail || j == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body = map[string]any{"results": files}
	case strings.HasPrefix(path, "/api/v3/files/"):
		p.mu.Lock()
		onFetch := p.onFetch
		p.mu.Unlock()
		if onFetch == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		onFetch()
		<-r.Context().Done()
		return
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// listing lists the user's own jobs, or those under the shared root.
func (p *platform) listing(shared bool) any {
	p.mu.Lock()
	defer p.mu.Unlock()
	var jobs []models.JobResponse
	for _, j := range p.jobs {
		if (j.folder != "") != shared {
			continue
		}
		job := models.JobResponse{ID: j.id, Name: cmp.Or(j.name, j.id), JobStatus: models.JobStatusContent{Status: "Completed"}}
		if !j.created.IsZero() {
			job.CreatedAt = j.created.UTC().Format(time.RFC3339)
		}
		if j.folder != "" {
			job.Folder = &models.JobFolder{ID: j.folder}
		}
		jobs = append(jobs, job)
	}
	return map[string]any{"results": jobs}
}

func (p *platform) serveTags(client, id string, w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		p.mu.Lock()
		p.readsBy[client]++
		p.tagReads = append(p.tagReads, id)
		n, failFrom, beforeRead := p.readsBy[client], p.failReadsFrom, p.beforeRead
		p.mu.Unlock()
		if beforeRead != nil {
			beforeRead(client, n)
		}
		if failFrom > 0 && n >= failFrom {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		tags := []api.JobTag{}
		for _, name := range p.tags(id) {
			tags = append(tags, api.JobTag{Name: name})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tags)
		return
	}
	var tag api.JobTag
	_ = json.NewDecoder(r.Body).Decode(&tag)
	put, claim := r.Method == http.MethodPost, strings.HasPrefix(tag.Name, config.StartedTag)
	change := "-" + tag.Name
	if put {
		change = "+" + tag.Name
	}
	p.mu.Lock()
	p.tagWrites = append(p.tagWrites, change)
	delay, hang := p.claimDelay[client], !put && p.hangDeletes
	p.mu.Unlock()
	if hang {
		<-r.Context().Done()
		return
	}
	if put && claim {
		time.Sleep(delay)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	j := p.job(id)
	switch {
	case !put && p.failDeletes > 0:
		p.failDeletes--
		w.WriteHeader(http.StatusInternalServerError)
		return
	case claim && put && p.refuseClaims:
		w.WriteHeader(http.StatusInternalServerError)
		return
	case j == nil:
	case !put:
		j.tags = slices.DeleteFunc(j.tags, func(s string) bool { return s == tag.Name })
	case !slices.Contains(j.tags, tag.Name):
		j.tags = append(j.tags, tag.Name)
		if claim && p.onClaim != nil {
			p.onClaim(tag.Name)
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

// job returns the job with this ID, or nil. The caller holds mu.
func (p *platform) job(id string) *fakeJob {
	for _, j := range p.jobs {
		if j.id == id {
			return j
		}
	}
	return nil
}

// tags returns the job's tags now.
func (p *platform) tags(id string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if j := p.job(id); j != nil {
		return slices.Clone(j.tags)
	}
	return nil
}

// set changes, under the lock, what the platform serves.
func (p *platform) set(change func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	change()
}

// listed returns how many times the clients listed a job's files.
func (p *platform) listed() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.listings {
		n += c
	}
	return n
}

// read returns, in order, the jobs whose completion times were looked up and
// the jobs whose tags were read.
func (p *platform) read() (lookups, tagReads []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.lookups), slices.Clone(p.tagReads)
}

// writes returns, in order, the tags put on ("+tag") and taken off ("-tag").
func (p *platform) writes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.tagWrites)
}

// puts returns how many times tag was put on a job.
func (p *platform) puts(tag string) int {
	n := 0
	for _, change := range p.writes() {
		if change == "+"+tag {
			n++
		}
	}
	return n
}
