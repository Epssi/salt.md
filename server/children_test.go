package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Fixture for the sub-pages tests: one workspace, an administrator (the first
// account), and two ordinary members.
type childrenFixture struct {
	s                          *Server
	ws                         string
	adminID, adminCookie       string
	memberID, memberCookie     string
	otherID, otherCookie       string
	outsiderID, outsiderCookie string
}

func newChildrenFixture(t *testing.T) *childrenFixture {
	t.Helper()
	f := &childrenFixture{s: testServer(t)}
	f.adminID, f.adminCookie = signedIn(t, f.s, "admin@example.com")
	f.ws = makeWorkspace(t, f.s, f.adminID)
	join := func(email string) (string, string) {
		id, cookie := signedIn(t, f.s, email)
		if _, err := f.s.db.Exec(`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES (?, ?, 'member')`, f.ws, id); err != nil {
			t.Fatalf("join: %v", err)
		}
		return id, cookie
	}
	f.memberID, f.memberCookie = join("member@example.com")
	f.otherID, f.otherCookie = join("other@example.com")
	// Signed in, but belongs to no workspace here.
	f.outsiderID, f.outsiderCookie = signedIn(t, f.s, "outsider@example.com")
	return f
}

// page inserts a page directly. visibility "private" is owned by owner.
func (f *childrenFixture) page(t *testing.T, id, parent, title, owner, visibility string, flags ...string) {
	t.Helper()
	var par any
	if parent != "" {
		par = parent
	}
	var trashed any
	tpl := 0
	for _, fl := range flags {
		switch fl {
		case "trashed":
			trashed = now()
		case "template":
			tpl = 1
		}
	}
	if _, err := f.s.db.Exec(`INSERT INTO pages (id, parent_id, title, content, position, created_at, updated_at, trashed_at, workspace_id, owner_id, visibility, type, is_template)
		VALUES (?, ?, ?, '[]', 0, ?, ?, ?, ?, ?, ?, 'doc', ?)`,
		id, par, title, now(), now(), trashed, f.ws, owner, visibility, tpl); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

func (f *childrenFixture) get(t *testing.T, path, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Cookie", cookie)
	f.s.ServeHTTP(rec, req)
	return rec
}

// children returns the ids the route lists, in order.
func (f *childrenFixture) children(t *testing.T, id, cookie string) []string {
	t.Helper()
	rec := f.get(t, "/api/pages/"+id+"/children", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET children of %s: %d %s", id, rec.Code, rec.Body.String())
	}
	var list []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ids := make([]string, 0, len(list))
	for _, p := range list {
		ids = append(ids, p.ID)
	}
	return ids
}

func sameIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestChildrenOfAPageYouCannotReadIs404(t *testing.T) {
	f := newChildrenFixture(t)
	f.page(t, "parent", "", "Parent", f.adminID, "workspace")
	f.page(t, "kid", "parent", "Kid", f.adminID, "workspace")
	f.page(t, "secret", "", "Secret", f.memberID, "private")
	f.page(t, "secret-kid", "secret", "Under the secret", f.memberID, "workspace")

	for name, tc := range map[string]struct{ id, cookie string }{
		"not a member of the workspace":   {"parent", f.outsiderCookie},
		"a private parent owned by other": {"secret", f.otherCookie},
		"a page that does not exist":      {"nope", f.adminCookie},
	} {
		rec := f.get(t, "/api/pages/"+tc.id+"/children", tc.cookie)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404", name, rec.Code)
		}
		// Whatever the reason, the answer is the same: it must not say which.
		if strings.Contains(rec.Body.String(), "Kid") || strings.Contains(rec.Body.String(), "Under the secret") {
			t.Errorf("%s: the 404 leaked a title: %s", name, rec.Body.String())
		}
	}
}

func TestChildrenDropAnotherMembersPrivatePage(t *testing.T) {
	f := newChildrenFixture(t)
	f.page(t, "parent", "", "Parent", f.adminID, "workspace")
	f.page(t, "open", "parent", "Open", f.adminID, "workspace")
	f.page(t, "mine", "parent", "Private of member", f.memberID, "private")

	if got := f.children(t, "parent", f.otherCookie); !sameIDs(got, []string{"open"}) {
		t.Errorf("another member sees %v, want only [open]", got)
	}
	// The owner of the private page sees it.
	if got := f.children(t, "parent", f.memberCookie); len(got) != 2 {
		t.Errorf("owner sees %v, want both children", got)
	}
}

func TestChildrenLeaveOutTheTrash(t *testing.T) {
	f := newChildrenFixture(t)
	f.page(t, "parent", "", "Parent", f.adminID, "workspace")
	f.page(t, "live", "parent", "Live", f.adminID, "workspace")
	f.page(t, "gone", "parent", "Gone", f.adminID, "workspace", "trashed")

	if got := f.children(t, "parent", f.memberCookie); !sameIDs(got, []string{"live"}) {
		t.Errorf("got %v, want only [live]", got)
	}
}

// The reason the route exists: GET /api/pages leaves a template and everything
// under it out, so the page list cannot say what a template contains.
func TestChildrenOfATemplateAndItsSubtree(t *testing.T) {
	f := newChildrenFixture(t)
	f.page(t, "tpl", "", "Template", f.adminID, "workspace", "template")
	f.page(t, "tpl-a", "tpl", "Part A", f.adminID, "workspace")
	f.page(t, "tpl-b", "tpl", "Part B", f.adminID, "workspace")
	f.page(t, "tpl-a1", "tpl-a", "Part A1", f.adminID, "workspace")

	rec := f.get(t, "/api/pages", f.memberCookie)
	for _, id := range []string{"tpl", "tpl-a", "tpl-b", "tpl-a1"} {
		if strings.Contains(rec.Body.String(), `"`+id+`"`) {
			t.Fatalf("%s is in /api/pages — the premise of the route is gone", id)
		}
	}

	if got := f.children(t, "tpl", f.memberCookie); len(got) != 2 {
		t.Errorf("template lists %v, want its two direct children", got)
	}
	// Direct children only: the grandchild belongs to Part A.
	if got := f.children(t, "tpl-a", f.memberCookie); !sameIDs(got, []string{"tpl-a1"}) {
		t.Errorf("Part A lists %v, want [tpl-a1]", got)
	}
}

func TestChildrenLeaveOutTemplatesHangingUnderAPage(t *testing.T) {
	f := newChildrenFixture(t)
	f.page(t, "parent", "", "Parent", f.adminID, "workspace")
	f.page(t, "normal", "parent", "Normal", f.adminID, "workspace")
	f.page(t, "stray", "parent", "Stray template", f.adminID, "workspace", "template")

	if got := f.children(t, "parent", f.memberCookie); !sameIDs(got, []string{"normal"}) {
		t.Errorf("got %v, want only [normal]", got)
	}
}

func TestChildrenAnAdministratorSeesOthersPrivatePages(t *testing.T) {
	f := newChildrenFixture(t)
	f.page(t, "parent", "", "Parent", f.adminID, "workspace")
	f.page(t, "open", "parent", "Open", f.adminID, "workspace")
	f.page(t, "private-kid", "parent", "Private of member", f.memberID, "private")

	got := f.children(t, "parent", f.adminCookie)
	if len(got) != 2 {
		t.Errorf("administrator sees %v, want both children", got)
	}
}
