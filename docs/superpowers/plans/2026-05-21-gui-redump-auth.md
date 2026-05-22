# GUI Redump Authentication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a GUI Redump tab that stores Redump credentials in SQLite and uses them for authenticated Redump SHA-1 lookups.

**Architecture:** Add SQLite helpers for plaintext Redump credentials and auth-aware Redump cache rows. Move Redump HTTP behavior into a small client that can do anonymous or authenticated quicksearch. Add a `Redump` tab to the existing Gio top bar and route saved credentials into the existing lookup pipeline.

**Tech Stack:** Go, Gio v0.9.0, modernc SQLite, net/http, net/http/cookiejar, httptest.

---

## File Map

| File | Purpose |
|------|---------|
| `tools/miniscram-gui/db.go` | Add `redump_auth` schema/helpers and auth-aware cache table/helpers. |
| `tools/miniscram-gui/db_test.go` | New DB tests for auth storage and auth-mode cache separation. |
| `tools/miniscram-gui/redump_client.go` | New Redump HTTP client: login, CSRF parsing, authenticated/anonymous quicksearch. |
| `tools/miniscram-gui/redump_client_test.go` | New httptest coverage for login and quicksearch behavior. |
| `tools/miniscram-gui/main.go` | Wire auth-aware lookup and add Redump tab state/actions/view dispatch. |
| `tools/miniscram-gui/redump_view.go` | New Gio widgets for the Redump settings tab. |
| `tools/miniscram-gui/redump_view_test.go` | New focused tests for caution text/helper state. |
| `tools/miniscram-gui/README.md` | Document Redump tab and plaintext credential storage. |

---

### Task 1: SQLite Redump Auth and Cache Mode

**Files:**
- Modify: `tools/miniscram-gui/db.go`
- Create: `tools/miniscram-gui/db_test.go`

- [ ] **Step 1: Write failing tests for credential storage and auth cache separation**

Create `tools/miniscram-gui/db_test.go`:

```go
package main

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestRedumpAuth_SaveLoadClear(t *testing.T) {
	db := newMemoryDB(t)

	if _, ok := redumpAuthGet(db); ok {
		t.Fatal("redumpAuthGet before save returned ok=true")
	}

	username, password := redumpTestCreds(t)
	redumpAuthPut(db, username, password)
	auth, ok := redumpAuthGet(db)
	if !ok {
		t.Fatal("redumpAuthGet after save returned ok=false")
	}
	if auth.Username != username {
		t.Errorf("Username = %q, want %q", auth.Username, username)
	}
	if auth.Password != password {
		t.Errorf("Password = %q, want env password", auth.Password)
	}
	if auth.UpdatedUnix == 0 {
		t.Error("UpdatedUnix = 0, want non-zero")
	}

	redumpAuthClear(db)
	if _, ok := redumpAuthGet(db); ok {
		t.Fatal("redumpAuthGet after clear returned ok=true")
	}
}

func TestRedumpCache_SeparatesAnonymousAndAuthenticated(t *testing.T) {
	db := newMemoryDB(t)
	const hash = "deadbeef"

	redumpPut(db, hash, "anon", &redumpEntry{State: "miss", CheckedUnix: time.Now().Unix()})
	redumpPut(db, hash, "auth", &redumpEntry{
		State:       "found",
		URL:         "http://redump.org/disc/47784/",
		Title:       "Fallout 4: Featured Music Selections",
		CheckedUnix: time.Now().Unix(),
	})

	anon, ok := redumpGet(db, hash, "anon")
	if !ok {
		t.Fatal("redumpGet anon returned ok=false")
	}
	if anon.State != "miss" {
		t.Errorf("anon.State = %q, want miss", anon.State)
	}

	auth, ok := redumpGet(db, hash, "auth")
	if !ok {
		t.Fatal("redumpGet auth returned ok=false")
	}
	if auth.State != "found" {
		t.Errorf("auth.State = %q, want found", auth.State)
	}
	if auth.Title != "Fallout 4: Featured Music Selections" {
		t.Errorf("auth.Title = %q", auth.Title)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
cd tools/miniscram-gui && /usr/local/go/bin/go test -run 'TestRedumpAuth|TestRedumpCache' ./...
```

Expected: build fails because `redumpAuthGet`, `redumpAuthPut`, `redumpAuthClear`, and new `redumpGet/redumpPut` signatures do not exist.

- [ ] **Step 3: Implement schema and helpers**

In `tools/miniscram-gui/db.go`, update `schema` by adding:

```go
CREATE TABLE IF NOT EXISTS redump_auth (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    username     TEXT NOT NULL,
    password     TEXT NOT NULL,
    updated_unix INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS redump_cache_v2 (
    hash         TEXT NOT NULL,
    auth_mode    TEXT NOT NULL,
    state        TEXT NOT NULL,
    url          TEXT,
    title        TEXT,
    checked_unix INTEGER NOT NULL,
    PRIMARY KEY (hash, auth_mode)
);
INSERT OR IGNORE INTO redump_cache_v2 (hash, auth_mode, state, url, title, checked_unix)
    SELECT hash, 'anon', state, url, title, checked_unix FROM redump_cache;
```

Add this type and helpers after `dbOpen`:

```go
type redumpAuth struct {
	Username    string
	Password    string
	UpdatedUnix int64
}

func redumpAuthGet(db *sql.DB) (redumpAuth, bool) {
	if db == nil {
		return redumpAuth{}, false
	}
	var a redumpAuth
	err := db.QueryRow(`SELECT username, password, updated_unix FROM redump_auth WHERE id = 1`).
		Scan(&a.Username, &a.Password, &a.UpdatedUnix)
	if err != nil {
		return redumpAuth{}, false
	}
	return a, true
}

func redumpAuthPut(db *sql.DB, username, password string) {
	if db == nil {
		return
	}
	_, _ = db.Exec(`
		INSERT INTO redump_auth (id, username, password, updated_unix)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			username = excluded.username,
			password = excluded.password,
			updated_unix = excluded.updated_unix
	`, username, password, time.Now().Unix())
}

func redumpAuthClear(db *sql.DB) {
	if db == nil {
		return
	}
	_, _ = db.Exec(`DELETE FROM redump_auth WHERE id = 1`)
}
```

Change cache helpers to:

```go
func redumpGet(db *sql.DB, hash, authMode string) (*redumpEntry, bool) {
	if db == nil {
		return nil, false
	}
	row := db.QueryRow(`SELECT state, COALESCE(url,''), COALESCE(title,''), checked_unix FROM redump_cache_v2 WHERE hash = ? AND auth_mode = ?`, hash, authMode)
	e := &redumpEntry{}
	if err := row.Scan(&e.State, &e.URL, &e.Title, &e.CheckedUnix); err != nil {
		return nil, false
	}
	return e, true
}

func redumpPut(db *sql.DB, hash, authMode string, e *redumpEntry) {
	if db == nil || e == nil {
		return
	}
	_, _ = db.Exec(`
		INSERT INTO redump_cache_v2 (hash, auth_mode, state, url, title, checked_unix)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(hash, auth_mode) DO UPDATE SET
			state = excluded.state,
			url = excluded.url,
			title = excluded.title,
			checked_unix = excluded.checked_unix
	`, hash, authMode, e.State, e.URL, e.Title, e.CheckedUnix)
}
```

Update all current call sites temporarily to pass `"anon"` so compilation succeeds. Later tasks will switch lookup to dynamic mode.

- [ ] **Step 4: Run tests**

Run:

```bash
cd tools/miniscram-gui && /usr/local/go/bin/go test -run 'TestRedumpAuth|TestRedumpCache' ./...
```

Expected: PASS.

- [ ] **Step 5: Run full GUI tests**

Run:

```bash
cd tools/miniscram-gui && /usr/local/go/bin/go test ./...
```

Expected: PASS.

---

### Task 2: Redump HTTP Client

**Files:**
- Create: `tools/miniscram-gui/redump_client.go`
- Create: `tools/miniscram-gui/redump_client_test.go`
- Modify: `tools/miniscram-gui/main.go`

- [ ] **Step 1: Write failing tests for CSRF parsing, login, and quicksearch**

Create `tools/miniscram-gui/redump_client_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
cd tools/miniscram-gui && /usr/local/go/bin/go test -run 'TestParseCSRFToken|TestRedumpClient' ./...
```

Expected: build fails because `parseCSRFToken` and `newRedumpClient` do not exist.

- [ ] **Step 3: Implement `redump_client.go`**

Create `tools/miniscram-gui/redump_client.go`:

```go
package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type redumpClient struct {
	forumBase string
	siteBase  string
	http      *http.Client
}

var csrfRe = regexp.MustCompile(`name="csrf_token"\s+value="([^"]+)"`)

func newRedumpClient(forumBase, siteBase string) *redumpClient {
	jar, _ := cookiejar.New(nil)
	return &redumpClient{
		forumBase: strings.TrimRight(forumBase, "/"),
		siteBase:  strings.TrimRight(siteBase, "/"),
		http: &http.Client{
			Timeout: 15 * time.Second,
			Jar:     jar,
		},
	}
}

func parseCSRFToken(body string) (string, bool) {
	m := csrfRe.FindStringSubmatch(body)
	if len(m) < 2 {
		return "", false
	}
	return m[1], true
}

func (c *redumpClient) Login(username, password string) error {
	resp, err := c.http.Get(c.forumBase + "/login/")
	if err != nil {
		return err
	}
	loginBodyBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	token, ok := parseCSRFToken(string(loginBodyBytes))
	if !ok {
		return errors.New("redump login csrf token not found")
	}

	form := url.Values{}
	form.Set("form_sent", "1")
	form.Set("redirect_url", c.forumBase+"/")
	form.Set("csrf_token", token)
	form.Set("req_username", username)
	form.Set("req_password", password)
	form.Set("save_pass", "1")
	form.Set("login", "Login")

	req, err := http.NewRequest(http.MethodPost, c.forumBase+"/login/", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	resp, err = c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)
	if !strings.Contains(body, "Logout") && !strings.Contains(body, "Logged in as") {
		return errors.New("redump login rejected")
	}
	return nil
}

func (c *redumpClient) Fetch(hash string) *redumpEntry {
	now := time.Now().Unix()
	req, err := http.NewRequest("GET", c.siteBase+"/discs/quicksearch/"+url.PathEscape(hash)+"/", nil)
	if err != nil {
		return &redumpEntry{State: "err", CheckedUnix: now}
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return &redumpEntry{State: "err", CheckedUnix: now}
	}
	defer resp.Body.Close()
	final := resp.Request.URL.String()
	if !strings.Contains(final, "/disc/") {
		return &redumpEntry{State: "miss", CheckedUnix: now}
	}
	body, _ := io.ReadAll(resp.Body)
	title := ""
	if m := titleRe.FindStringSubmatch(string(body)); len(m) > 1 {
		t := strings.TrimSpace(m[1])
		t = strings.SplitN(t, "&bull;", 2)[0]
		title = strings.TrimSpace(t)
	}
	return &redumpEntry{State: "found", URL: final, Title: title, CheckedUnix: now}
}
```

In `tools/miniscram-gui/main.go`, replace the body of `redumpFetch(hash string)` with:

```go
func redumpFetch(hash string) *redumpEntry {
	return newRedumpClient("http://forum.redump.org", "http://redump.org").Fetch(hash)
}
```

- [ ] **Step 4: Run client tests**

Run:

```bash
cd tools/miniscram-gui && /usr/local/go/bin/go test -run 'TestParseCSRFToken|TestRedumpClient' ./...
```

Expected: PASS.

---

### Task 3: Auth-Aware Lookup Pipeline

**Files:**
- Modify: `tools/miniscram-gui/main.go`
- Create/Modify: `tools/miniscram-gui/redump_lookup_test.go`

- [ ] **Step 1: Write failing test that authenticated lookup ignores anonymous miss**

Create `tools/miniscram-gui/redump_lookup_test.go`:

```go
package main

import (
	"testing"
	"time"
)

func TestLookupUsesAuthenticatedCacheWhenCredentialsExist(t *testing.T) {
	db := newMemoryDB(t)
	const hash = "deadbeef"
	redumpPut(db, hash, "anon", &redumpEntry{State: "miss", CheckedUnix: time.Now().Unix()})
	redumpPut(db, hash, "auth", &redumpEntry{
		State:       "found",
		URL:         "http://redump.org/disc/47784/",
		Title:       "Fallout 4: Featured Music Selections",
		CheckedUnix: time.Now().Unix(),
	})
	username, password := redumpTestCreds(t)
	redumpAuthPut(db, username, password)

	m := &model{db: db, redump: map[string]*redumpEntry{}}
	m.lookup([]string{hash})

	got := m.redump[hash]
	if got == nil {
		t.Fatal("m.redump[hash] is nil")
	}
	if got.State != "found" {
		t.Fatalf("State = %q, want found", got.State)
	}
	if got.Title != "Fallout 4: Featured Music Selections" {
		t.Errorf("Title = %q", got.Title)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
cd tools/miniscram-gui && /usr/local/go/bin/go test -run TestLookupUsesAuthenticatedCacheWhenCredentialsExist ./...
```

Expected: FAIL because `lookup` still uses anonymous cache mode.

- [ ] **Step 3: Update lookup to choose auth mode**

In `model.lookup`, compute auth mode once:

```go
auth, hasAuth := redumpAuthGet(m.db)
authMode := "anon"
if hasAuth {
	authMode = "auth"
}
```

Use `redumpGet(m.db, h, authMode)` and `redumpPut(m.db, h, authMode, e)`.

For network lookup:

```go
var e *redumpEntry
if hasAuth {
	client := newRedumpClient("http://forum.redump.org", "http://redump.org")
	if err := client.Login(auth.Username, auth.Password); err != nil {
		e = &redumpEntry{State: "err", CheckedUnix: time.Now().Unix()}
	} else {
		e = client.Fetch(h)
	}
} else {
	e = redumpFetch(h)
}
```

Keep the existing pending/invalidate flow.

- [ ] **Step 4: Run lookup test**

Run:

```bash
cd tools/miniscram-gui && /usr/local/go/bin/go test -run TestLookupUsesAuthenticatedCacheWhenCredentialsExist ./...
```

Expected: PASS.

- [ ] **Step 5: Run all GUI tests**

Run:

```bash
cd tools/miniscram-gui && /usr/local/go/bin/go test ./...
```

Expected: PASS.

---

### Task 4: Redump Tab UI

**Files:**
- Modify: `tools/miniscram-gui/main.go`
- Create: `tools/miniscram-gui/redump_view.go`
- Create: `tools/miniscram-gui/redump_view_test.go`

- [ ] **Step 1: Write failing test for caution text**

Create `tools/miniscram-gui/redump_view_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func TestRedumpPlaintextCautionText(t *testing.T) {
	got := redumpPlaintextCautionText()
	if !strings.Contains(got, "plaintext") {
		t.Fatalf("caution text = %q, want plaintext warning", got)
	}
	if !strings.Contains(got, "SQLite") {
		t.Fatalf("caution text = %q, want SQLite mention", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
cd tools/miniscram-gui && /usr/local/go/bin/go test -run TestRedumpPlaintextCautionText ./...
```

Expected: build fails because `redumpPlaintextCautionText` does not exist.

- [ ] **Step 3: Add Redump view state and widgets**

In `tools/miniscram-gui/main.go`, extend `model`:

```go
redumpUsername string
redumpPassword string
redumpStatus   string
```

At startup after `mdl.queue = newQueueModel()`, load saved auth:

```go
if auth, ok := redumpAuthGet(mdl.db); ok {
	mdl.redumpUsername = auth.Username
	mdl.redumpPassword = auth.Password
	mdl.redumpStatus = "Saved"
} else {
	mdl.redumpStatus = "Not configured"
}
```

Add top-bar widgets in `loop`:

```go
redumpBtn widget.Clickable
redumpUserEditor widget.Editor
redumpPassEditor widget.Editor
redumpSaveBtn widget.Clickable
redumpTestBtn widget.Clickable
redumpClearBtn widget.Clickable
```

Initialize editors before the event loop:

```go
redumpUserEditor.SingleLine = true
redumpPassEditor.SingleLine = true
redumpPassEditor.Mask = '*'
redumpUserEditor.SetText(mdl.redumpUsername)
redumpPassEditor.SetText(mdl.redumpPassword)
```

Update top bar call/signature to include Redump button and render:

```go
layout.Rigid(tabButton(b.th, b.redumpBtn, "Redump", b.mdl.view == "redump"))
```

Handle clicks:

```go
if redumpBtn.Clicked(gtx) {
	mdl.view = "redump"
}
if redumpSaveBtn.Clicked(gtx) {
	u := strings.TrimSpace(redumpUserEditor.Text())
	p := redumpPassEditor.Text()
	if u == "" || p == "" {
		mdl.redumpStatus = "Username and password required"
	} else {
		redumpAuthPut(mdl.db, u, p)
		mdl.redumpUsername = u
		mdl.redumpPassword = p
		mdl.redumpStatus = "Saved"
	}
}
if redumpClearBtn.Clicked(gtx) {
	redumpAuthClear(mdl.db)
	mdl.redumpUsername = ""
	mdl.redumpPassword = ""
	redumpUserEditor.SetText("")
	redumpPassEditor.SetText("")
	mdl.redumpStatus = "Not configured"
}
if redumpTestBtn.Clicked(gtx) {
	u := strings.TrimSpace(redumpUserEditor.Text())
	p := redumpPassEditor.Text()
	if u == "" || p == "" {
		mdl.redumpStatus = "Username and password required"
	} else if err := newRedumpClient("http://forum.redump.org", "http://redump.org").Login(u, p); err != nil {
		mdl.redumpStatus = "Login failed"
	} else {
		mdl.redumpStatus = "Login OK"
	}
}
```

Create `tools/miniscram-gui/redump_view.go`:

```go
package main

import (
	"gioui.org/layout"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

func redumpPlaintextCautionText() string {
	return "Caution: Redump credentials are stored in plaintext in the local SQLite database."
}

func redumpView(th *material.Theme, mdl *model, user, pass *widget.Editor, save, test, clear *widget.Clickable) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 28, Left: 32, Right: 32}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(th, unit.Sp(20), "Redump")
					l.Color = text1
					return l.Layout(gtx)
				}),
				layout.Rigid(spacer(0, 16)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(th, unit.Sp(12), "Username")
					l.Color = text2
					return l.Layout(gtx)
				}),
				layout.Rigid(material.Editor(th, user, "Redump username").Layout),
				layout.Rigid(spacer(0, 10)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(th, unit.Sp(12), "Password")
					l.Color = text2
					return l.Layout(gtx)
				}),
				layout.Rigid(material.Editor(th, pass, "Redump password").Layout),
				layout.Rigid(spacer(0, 12)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(panelButton(th, save, "Save credentials")),
						layout.Rigid(spacer(8, 0)),
						layout.Rigid(panelButton(th, test, "Test login")),
						layout.Rigid(spacer(8, 0)),
						layout.Rigid(panelButton(th, clear, "Clear credentials")),
					)
				}),
				layout.Rigid(spacer(0, 12)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(th, unit.Sp(12), mdl.redumpStatus)
					l.Color = text2
					return l.Layout(gtx)
				}),
				layout.Rigid(spacer(0, 8)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(th, unit.Sp(10), redumpPlaintextCautionText())
					l.Color = text3
					l.Alignment = text.Start
					return l.Layout(gtx)
				}),
			)
		})
	}
}
```

In body/view dispatch, render `redumpView` when `mdl.view == "redump"`.

- [ ] **Step 4: Run Redump view test**

Run:

```bash
cd tools/miniscram-gui && /usr/local/go/bin/go test -run TestRedumpPlaintextCautionText ./...
```

Expected: PASS.

- [ ] **Step 5: Run full GUI tests**

Run:

```bash
cd tools/miniscram-gui && /usr/local/go/bin/go test ./...
```

Expected: PASS.

---

### Task 5: README and Final Verification

**Files:**
- Modify: `tools/miniscram-gui/README.md`

- [ ] **Step 1: Update README**

In `tools/miniscram-gui/README.md`, update the feature list to say Redump lookups can use saved credentials:

```markdown
- Looks each track's SHA-1 up at redump.org (`/discs/quicksearch/<hash>/`)
  with an "Open ↗" link when matched. The Redump tab can store credentials
  in the local SQLite database so lookups run as an authenticated Redump user.
```

Update the storage table:

```markdown
| Redump credentials | `$XDG_DATA_HOME/miniscram-gui/db.sqlite` (`redump_auth`, plaintext) |
```

- [ ] **Step 2: Run GUI tests**

Run:

```bash
cd tools/miniscram-gui && /usr/local/go/bin/go test ./...
```

Expected: PASS.

- [ ] **Step 3: Run root tests**

Run:

```bash
/usr/local/go/bin/go test ./...
```

Expected: PASS.

- [ ] **Step 4: Check git diff**

Run:

```bash
git diff --stat
git status --short
```

Expected: only files listed in the File Map are modified/created, plus the design and plan docs if they are part of the branch.
