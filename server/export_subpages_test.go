package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const subpagesBody = `[{"id":"b1","type":"paragraph","props":{},"content":[{"type":"text","text":"Intro","styles":{}}],"children":[]},` +
	`{"id":"b2","type":"subpages","props":{},"content":[],"children":[]}]`

// A parent whose body holds a Sub-pages block, with one child everybody may
// see and one that is private to a member.
func (f *childrenFixture) parentWithBlock(t *testing.T) {
	t.Helper()
	f.page(t, "parent", "", "Parent", f.adminID, "workspace")
	f.page(t, "open", "parent", "Open child", f.adminID, "workspace")
	f.page(t, "hidden", "parent", "Private child", f.memberID, "private")
	if _, err := f.s.db.Exec(`UPDATE pages SET content = ? WHERE id = 'parent'`, subpagesBody); err != nil {
		t.Fatalf("set content: %v", err)
	}
}

func TestSubpagesExportListsWhatTheReaderMaySee(t *testing.T) {
	f := newChildrenFixture(t)
	f.parentWithBlock(t)

	t.Run("markdown", func(t *testing.T) {
		md := f.get(t, "/api/export/parent", f.otherCookie).Body.String()
		if !strings.Contains(md, "- [Open child](/p/open)") {
			t.Errorf("no link to the visible child:\n%s", md)
		}
		if strings.Contains(md, "Private child") || strings.Contains(md, "/p/hidden") {
			t.Errorf("another member's private page is listed:\n%s", md)
		}
		if !strings.Contains(md, "Intro") {
			t.Errorf("the rest of the body is gone:\n%s", md)
		}
		// Its owner gets the whole list.
		if own := f.get(t, "/api/export/parent", f.memberCookie).Body.String(); !strings.Contains(own, "/p/hidden") {
			t.Errorf("the owner does not get their own private page:\n%s", own)
		}
	})

	for name, path := range map[string]string{
		"html download": "/api/export/parent?format=html",
		"print view":    "/api/export/parent?format=html&print=1",
	} {
		t.Run(name, func(t *testing.T) {
			h := f.get(t, path, f.otherCookie).Body.String()
			if !strings.Contains(h, `<a href="/p/open">Open child</a>`) {
				t.Errorf("no link to the visible child")
			}
			if strings.Contains(h, "Private child") || strings.Contains(h, "/p/hidden") {
				t.Errorf("another member's private page is listed")
			}
		})
	}
}

// The visitor of a shared page was given that page and nothing under it.
func TestSubpagesPublicViewListsNothing(t *testing.T) {
	f := newChildrenFixture(t)
	f.parentWithBlock(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/pages/parent/share", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", f.adminCookie)
	f.s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("share: %d %s", rec.Code, rec.Body.String())
	}
	var shared struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&shared); err != nil || shared.Token == "" {
		t.Fatalf("no token: %v", err)
	}

	// No cookie at all: an anonymous visitor.
	pub := httptest.NewRecorder()
	f.s.ServeHTTP(pub, httptest.NewRequest("GET", "/public/"+shared.Token, nil))
	if pub.Code != http.StatusOK {
		t.Fatalf("public view: %d", pub.Code)
	}
	body := pub.Body.String()
	if !strings.Contains(body, "Intro") {
		t.Fatalf("the shared page itself did not render")
	}
	for _, leak := range []string{"Open child", "Private child", "/p/open", "/p/hidden"} {
		if strings.Contains(body, leak) {
			t.Errorf("anonymous view contains %q", leak)
		}
	}
}

// A block with nothing to list leaves no trace in the export.
func TestSubpagesBlockWithNoChildrenPrintsNothing(t *testing.T) {
	f := newChildrenFixture(t)
	f.page(t, "lonely", "", "Lonely", f.adminID, "workspace")
	if _, err := f.s.db.Exec(`UPDATE pages SET content = ? WHERE id = 'lonely'`, subpagesBody); err != nil {
		t.Fatal(err)
	}
	md := f.get(t, "/api/export/lonely", f.adminCookie).Body.String()
	if strings.Contains(md, "- [") {
		t.Errorf("an empty list printed items:\n%s", md)
	}
	if got := strings.TrimSpace(md); got != "# Lonely\n\nIntro" {
		t.Errorf("unexpected export: %q", got)
	}
}
