package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseCSRFToken(t *testing.T) {
	html := `<input type="hidden" name="csrf_token" value="abc123" />`
	got, ok := parseCSRFToken(html)
	if !ok {
		t.Fatal("parseCSRFToken ok=false")
	}
	if got != "abc123" {
		t.Errorf("token = %q, want abc123", got)
	}
}

func TestRedumpClient_LoginSuccess(t *testing.T) {
	username, password := redumpTestCreds(t)
	var postedUser, postedPass string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login/" && r.Method == http.MethodGet:
			fmt.Fprint(w, `<input type="hidden" name="csrf_token" value="tok" />`)
		case r.URL.Path == "/login/" && r.Method == http.MethodPost:
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			postedUser = r.Form.Get("req_username")
			postedPass = r.Form.Get("req_password")
			http.SetCookie(w, &http.Cookie{Name: "punbb_cookie", Value: "ok", Path: "/"})
			fmt.Fprintf(w, `Logged in as <strong>%s</strong>. <a href="/logout/">Logout</a>`, username)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newRedumpClient(srv.URL, srv.URL)
	if err := c.Login(username, password); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if postedUser != username || postedPass != password {
		t.Fatalf("posted credentials = %q/%q", postedUser, postedPass)
	}
}

func TestRedumpClient_QuicksearchAuthenticated(t *testing.T) {
	username, password := redumpTestCreds(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login/":
			if r.Method == http.MethodGet {
				fmt.Fprint(w, `<input type="hidden" name="csrf_token" value="tok" />`)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "punbb_cookie", Value: "ok", Path: "/"})
			fmt.Fprintf(w, `Logged in as <strong>%s</strong>. <a href="/logout/">Logout</a>`, username)
		case strings.HasPrefix(r.URL.Path, "/discs/quicksearch/"):
			if _, err := r.Cookie("punbb_cookie"); err != nil {
				http.Redirect(w, r, "/discs/quicksearch/miss/", http.StatusFound)
				return
			}
			http.Redirect(w, r, "/disc/47784/", http.StatusFound)
		case r.URL.Path == "/disc/47784/":
			fmt.Fprint(w, `<title>redump.org &bull; Fallout 4: Featured Music Selections</title>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newRedumpClient(srv.URL, srv.URL)
	if err := c.Login(username, password); err != nil {
		t.Fatalf("Login: %v", err)
	}
	e := c.Fetch("abc")
	if e.State != "found" {
		t.Fatalf("State = %q, want found", e.State)
	}
	if e.Title != "Fallout 4: Featured Music Selections" {
		t.Errorf("Title = %q", e.Title)
	}
}
