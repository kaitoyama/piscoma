package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
)

const sessionCookieName = "isucoma_session_id"

// sessionCookieMaxAge は Cookie の有効期間（旧 gorilla/sessions 実装の7日を踏襲）。
const sessionCookieMaxAge = 86400 * 7

const authContextKey = "isucoma_auth_user"

// authUser is the minimal identity resolved from the DB for the current request.
type authUser struct {
	ID   int64
	Role string
}

// sessionRow is one row of the sessions table.
type sessionRow struct {
	Token     string    `db:"token"`
	UserID    int64     `db:"user_id"`
	CreatedAt time.Time `db:"created_at"`
}

// newSessionToken generates an opaque, unguessable session token
// (crypto/rand, 32 bytes -> 64 hex chars, matches sessions.token CHAR(64)).
func newSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// establishSession はログイン成功・登録成功時に呼ばれる。新しいセッショントークンを
// 発行して sessions テーブルへ INSERT し、Cookie（HttpOnly）として払い出す
// （auth スペック「セッションの動作」）。
func establishSession(c echo.Context, u User) error {
	db := getDB(c)

	token, err := newSessionToken()
	if err != nil {
		return err
	}

	if _, err := db.Exec("INSERT INTO sessions (token, user_id) VALUES (?, ?)", token, u.ID); err != nil {
		return err
	}

	c.SetCookie(&http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   sessionCookieMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	// このリクエスト内で以後 currentUserID/currentUserRole が呼ばれる場合に備えて
	// キャッシュしておく（余計なDB往復を増やさない）。
	c.Set(authContextKey, &authUser{ID: u.ID, Role: u.Role})
	return nil
}

func loadAuthUser(c echo.Context) *authUser {
	if v := c.Get(authContextKey); v != nil {
		au, _ := v.(*authUser) // 型付きnilもありうる（=「解決済み・未認証」を意味する）
		return au
	}

	au := lookupAuthUser(c)
	c.Set(authContextKey, au)
	return au
}

func lookupAuthUser(c echo.Context) *authUser {
	cookie, err := c.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}

	db := getDB(c)

	var sess sessionRow
	if err := db.Get(&sess, "SELECT token, user_id, created_at FROM sessions WHERE token = ?", cookie.Value); err != nil {
		return nil
	}

	var u User
	if err := db.Get(&u, "SELECT id, email, password_hash, display_name, role, preference_category, created_at FROM users WHERE id = ?", sess.UserID); err != nil {
		return nil
	}

	return &authUser{ID: u.ID, Role: u.Role}
}

// currentUserID はセッションから user_id を取り出す。未認証なら ok=false。
func currentUserID(c echo.Context) (int64, bool) {
	au := loadAuthUser(c)
	if au == nil {
		return 0, false
	}
	return au.ID, true
}

// currentUserRole はセッションから role を取り出す。未認証なら ok=false。
func currentUserRole(c echo.Context) (string, bool) {
	au := loadAuthUser(c)
	if au == nil {
		return "", false
	}
	return au.Role, true
}
