package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const sessionCookie = "leopic_session"
const sessionTTL = 30 * 24 * time.Hour

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func constEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// createToken 生成 Sanctum 风格 token：返回 "{id}|{plain40}"，库里只存 sha256(plain)。
func createToken(db *sql.DB, name string) (string, error) {
	plain := randomString(40)
	res, err := db.Exec("INSERT INTO tokens (name, token_hash, created_at) VALUES (?,?,?)",
		name, sha256hex(plain), time.Now().Unix())
	if err != nil {
		return "", err
	}
	id, _ := res.LastInsertId()
	return strconv.FormatInt(id, 10) + "|" + plain, nil
}

// checkToken 与 Sanctum 2.x 一致：含 '|' 时按 id 取记录比 hash，否则按 hash 查。
func checkToken(db *sql.DB, raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	var id int64
	var hash string
	if i := strings.IndexByte(raw, '|'); i >= 0 {
		n, err := strconv.ParseInt(raw[:i], 10, 64)
		if err != nil {
			return false
		}
		if err := db.QueryRow("SELECT id, token_hash FROM tokens WHERE id=?", n).Scan(&id, &hash); err != nil {
			return false
		}
		if !constEq(hash, sha256hex(raw[i+1:])) {
			return false
		}
	} else {
		if err := db.QueryRow("SELECT id FROM tokens WHERE token_hash=?", sha256hex(raw)).Scan(&id); err != nil {
			return false
		}
	}
	db.Exec("UPDATE tokens SET last_used_at=? WHERE id=?", time.Now().Unix(), id)
	return true
}

func createSession(db *sql.DB) (string, error) {
	v := randomString(48)
	now := time.Now()
	db.Exec("DELETE FROM sessions WHERE expires_at<?", now.Unix())
	_, err := db.Exec("INSERT INTO sessions (id_hash, expires_at) VALUES (?,?)", sha256hex(v), now.Add(sessionTTL).Unix())
	return v, err
}

func checkSession(db *sql.DB, r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	var exp int64
	if err := db.QueryRow("SELECT expires_at FROM sessions WHERE id_hash=?", sha256hex(c.Value)).Scan(&exp); err != nil {
		return false
	}
	return exp > time.Now().Unix()
}

func deleteSession(db *sql.DB, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		db.Exec("DELETE FROM sessions WHERE id_hash=?", sha256hex(c.Value))
	}
}

func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:]), true
	}
	return "", true
}

// authed：带 Authorization 头时只认 token（与 lsky 一致），否则认 session。
func authed(db *sql.DB, r *http.Request) bool {
	if tok, has := bearer(r); has {
		return checkToken(db, tok)
	}
	return checkSession(db, r)
}

// failLimiter：全局登录失败限流，每分钟最多 limit 次失败。
type failLimiter struct {
	mu    sync.Mutex
	limit int
	times []time.Time
}

var errLimited = errors.New("Too Many Attempts.")

func (l *failLimiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := time.Now().Add(-time.Minute)
	kept := l.times[:0]
	for _, t := range l.times {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	l.times = kept
	return len(l.times) < l.limit
}

func (l *failLimiter) fail() {
	l.mu.Lock()
	l.times = append(l.times, time.Now())
	l.mu.Unlock()
}
