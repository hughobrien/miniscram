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
var sha1HexRe = regexp.MustCompile(`\b[0-9a-fA-F]{40}\b`)

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

func parseRedumpDiscSHA1s(body string) []string {
	matches := sha1HexRe.FindAllString(body, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, match := range matches {
		h := strings.ToLower(match)
		if seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
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
	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)
	title := ""
	if m := titleRe.FindStringSubmatch(body); len(m) > 1 {
		t := strings.TrimSpace(m[1])
		t = strings.SplitN(t, "&bull;", 2)[0]
		title = strings.TrimSpace(t)
	}
	return &redumpEntry{
		State:       "found",
		URL:         final,
		Title:       title,
		CheckedUnix: now,
		TrackSHA1s:  parseRedumpDiscSHA1s(body),
	}
}
