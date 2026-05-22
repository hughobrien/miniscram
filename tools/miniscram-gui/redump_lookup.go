package main

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"
)

type redumpEntry struct {
	State       string `json:"state"` // "found" | "miss" | "err" | "pending"
	URL         string `json:"url,omitempty"`
	Title       string `json:"title,omitempty"`
	CheckedUnix int64  `json:"checked_unix"`
}

var titleRe = regexp.MustCompile(`<title>redump\.org\s*&bull;\s*([^<]+?)\s*</title>`)

type redumpFetcher interface {
	Fetch(hash string) *redumpEntry
}

type redumpFetchFunc func(hash string) *redumpEntry

func (f redumpFetchFunc) Fetch(hash string) *redumpEntry {
	return f(hash)
}

type redumpLookupService struct {
	login     func(username, password string) (redumpFetcher, error)
	anonFetch redumpFetcher
}

func defaultRedumpLookupService() *redumpLookupService {
	return &redumpLookupService{
		login: func(username, password string) (redumpFetcher, error) {
			c := newRedumpClient("http://forum.redump.org", "http://redump.org")
			if err := c.Login(username, password); err != nil {
				return nil, err
			}
			return c, nil
		},
		anonFetch: redumpFetchFunc(redumpFetch),
	}
}

func redumpFetch(hash string) *redumpEntry {
	return newRedumpClient("http://forum.redump.org", "http://redump.org").Fetch(hash)
}

func redumpAuthMode(db *sql.DB) string {
	if _, ok := redumpAuthGet(db); ok {
		return "auth"
	}
	return "anon"
}

func redumpCacheKey(hash, authMode string) string {
	return fmt.Sprintf("%s:%s", authMode, hash)
}

func (m *model) clearRedumpMemory() {
	m.redumpMu.Lock()
	defer m.redumpMu.Unlock()
	m.redump = map[string]*redumpEntry{}
}

func (m *model) lookup(hashes []string) {
	svc := m.redumpLookup
	if svc == nil {
		svc = defaultRedumpLookupService()
	}
	svc.lookup(m, hashes)
}

func (s *redumpLookupService) lookup(m *model, hashes []string) {
	auth, hasAuth := redumpAuthGet(m.db)
	authMode := "anon"
	if hasAuth {
		authMode = "auth"
	}
	var (
		authFetcher    redumpFetcher
		authLoginErr   error
		authLoginTried bool
	)
	for _, h := range hashes {
		cacheKey := redumpCacheKey(h, authMode)
		if e, ok := redumpGet(m.db, h, authMode); ok {
			m.redumpMu.Lock()
			m.redump[cacheKey] = e
			m.redumpMu.Unlock()
			if m.invalidate != nil {
				m.invalidate()
			}
			continue
		}
		m.redumpMu.Lock()
		if existing, ok := m.redump[cacheKey]; ok && existing != nil && existing.State != "" && existing.State != "pending" {
			m.redumpMu.Unlock()
			continue
		}
		m.redump[cacheKey] = &redumpEntry{State: "pending"}
		m.redumpMu.Unlock()
		if m.invalidate != nil {
			m.invalidate()
		}

		var e *redumpEntry
		if hasAuth {
			if !authLoginTried {
				if s.login == nil {
					authLoginErr = errors.New("redump login unavailable")
				} else {
					authFetcher, authLoginErr = s.login(auth.Username, auth.Password)
				}
				authLoginTried = true
			}
			if authLoginErr != nil || authFetcher == nil {
				e = &redumpEntry{State: "err", CheckedUnix: time.Now().Unix()}
			} else {
				e = authFetcher.Fetch(h)
			}
		} else if s.anonFetch != nil {
			e = s.anonFetch.Fetch(h)
		} else {
			e = &redumpEntry{State: "err", CheckedUnix: time.Now().Unix()}
		}
		redumpPut(m.db, h, authMode, e)
		m.redumpMu.Lock()
		m.redump[cacheKey] = e
		m.redumpMu.Unlock()
		if m.invalidate != nil {
			m.invalidate()
		}
	}
}
