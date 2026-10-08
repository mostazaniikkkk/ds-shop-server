package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Panel users (data/users.json) and cookie sessions.

const (
	pbkdf2Iter     = 120000
	sessionCookie  = "dsstore_session"
	sessionTimeout = 12 * time.Hour
)

type User struct {
	Name string `json:"name"`
	Salt string `json:"salt"`
	Hash string `json:"hash"`
	Iter int    `json:"iter"`
}

type Users struct {
	mu   sync.Mutex
	path string
	list []User
}

func OpenUsers(dir string) (*Users, error) {
	u := &Users{path: filepath.Join(dir, "users.json")}
	b, err := os.ReadFile(u.path)
	if errors.Is(err, os.ErrNotExist) {
		return u, nil
	}
	if err != nil {
		return nil, err
	}
	return u, json.Unmarshal(b, &u.list)
}

func (u *Users) saveLocked() error {
	b, err := json.MarshalIndent(u.list, "", "  ")
	if err != nil {
		return err
	}
	tmp := u.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, u.path)
}

func (u *Users) Count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.list)
}

func (u *Users) Names() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	var n []string
	for _, x := range u.list {
		n = append(n, x.Name)
	}
	sort.Strings(n)
	return n
}

func hashPassword(pass string, salt []byte, iter int) []byte {
	return pbkdf2SHA256([]byte(pass), salt, iter, 32)
}

// SetPassword creates the user or changes their password.
func (u *Users) SetPassword(name, pass string) error {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, " \t") {
		return errors.New("el usuario no puede estar vacio ni tener espacios")
	}
	if len(pass) < 6 {
		return errors.New("la contrasena tiene que tener al menos 6 caracteres")
	}
	salt := make([]byte, 16)
	rand.Read(salt)
	rec := User{Name: name, Salt: hex.EncodeToString(salt), Hash: hex.EncodeToString(hashPassword(pass, salt, pbkdf2Iter)), Iter: pbkdf2Iter}
	u.mu.Lock()
	defer u.mu.Unlock()
	for i := range u.list {
		if u.list[i].Name == name {
			u.list[i] = rec
			return u.saveLocked()
		}
	}
	u.list = append(u.list, rec)
	return u.saveLocked()
}

func (u *Users) Delete(name string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.list) <= 1 {
		return errors.New("tiene que quedar al menos un usuario")
	}
	for i := range u.list {
		if u.list[i].Name == name {
			u.list = append(u.list[:i], u.list[i+1:]...)
			return u.saveLocked()
		}
	}
	return os.ErrNotExist
}

func (u *Users) Check(name, pass string) bool {
	u.mu.Lock()
	var rec *User
	for i := range u.list {
		if u.list[i].Name == name {
			r := u.list[i]
			rec = &r
		}
	}
	u.mu.Unlock()
	if rec == nil {
		// same cost even if the user does not exist
		hashPassword(pass, []byte("0123456789abcdef"), pbkdf2Iter)
		return false
	}
	salt, _ := hex.DecodeString(rec.Salt)
	want, _ := hex.DecodeString(rec.Hash)
	return subtle.ConstantTimeCompare(hashPassword(pass, salt, rec.Iter), want) == 1
}

// pbkdf2SHA256 (RFC 8018), to avoid depending on external packages.
func pbkdf2SHA256(pass, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, pass)
	var out []byte
	for block := uint32(1); len(out) < keyLen; block++ {
		prf.Reset()
		prf.Write(salt)
		prf.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u := prf.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iter; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

// ---------------------------------------------------------------- sessions

type sessions struct {
	mu sync.Mutex
	m  map[string]*session
}

type session struct {
	user    string
	expires time.Time
}

func (s *sessions) create(user string) string {
	b := make([]byte, 32)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]*session{}
	}
	s.m[tok] = &session{user: user, expires: time.Now().Add(sessionTimeout)}
	return tok
}

// get returns the session's user and extends its expiry.
func (s *sessions) get(tok string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.m[tok]
	if !ok || time.Now().After(ss.expires) {
		delete(s.m, tok)
		return "", false
	}
	ss.expires = time.Now().Add(sessionTimeout)
	return ss.user, true
}

func (s *sessions) drop(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, tok)
}

// dropUser closes a user's sessions (when deleting them or changing their password).
func (s *sessions) dropUser(user, except string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.m {
		if v.user == user && k != except {
			delete(s.m, k)
		}
	}
}

type ctxKey struct{}

// auth requires a logged-in session and rejects POSTs from other origins.
func (a *App) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		user, ok := "", false
		if err == nil {
			user, ok = a.sessions.get(c.Value)
		}
		if !ok {
			to := "/admin/login"
			if r.Method == http.MethodGet && r.URL.Path != "/admin/" {
				to += "?next=" + url.QueryEscape(r.URL.RequestURI())
			}
			http.Redirect(w, r, to, http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost && !sameOrigin(r) {
			http.Error(w, "origen no permitido", http.StatusForbidden)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, user)))
	}
}

func currentUser(r *http.Request) string {
	u, _ := r.Context().Value(ctxKey{}).(string)
	return u
}

func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		o = r.Header.Get("Referer")
	}
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

func (a *App) loginPage(w http.ResponseWriter, r *http.Request) {
	a.renderPlain(w, "login.html", map[string]any{
		"Name": a.store.Snapshot().Name,
		"Next": r.URL.Query().Get("next"),
		"Err":  r.URL.Query().Get("err"),
	})
}

func (a *App) loginPost(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "origen no permitido", http.StatusForbidden)
		return
	}
	user, pass := strings.TrimSpace(r.FormValue("user")), r.FormValue("pass")
	next := r.FormValue("next")
	if !strings.HasPrefix(next, "/admin/") || strings.HasPrefix(next, "//") {
		next = "/admin/"
	}
	if !a.users.Check(user, pass) {
		time.Sleep(700 * time.Millisecond) // slows down blind guessing
		http.Redirect(w, r, "/admin/login?err="+url.QueryEscape("Usuario o contraseña incorrectos")+"&next="+url.QueryEscape(next), http.StatusSeeOther)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: a.sessions.create(user), Path: "/admin/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.sessions.drop(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/admin/", MaxAge: -1})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// ---------------------------------------------------------------- users page

func (a *App) usersPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "users.html", page{Users: a.users.Names()})
}

func (a *App) usersAdd(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	for _, n := range a.users.Names() {
		if n == name {
			back(w, r, "/admin/users", errors.New("ese usuario ya existe"), "")
			return
		}
	}
	back(w, r, "/admin/users", a.users.SetPassword(name, r.FormValue("pass")), "Usuario creado")
}

func (a *App) usersPassword(w http.ResponseWriter, r *http.Request) {
	me := currentUser(r)
	if !a.users.Check(me, r.FormValue("old")) {
		back(w, r, "/admin/users", errors.New("la contraseña actual no es correcta"), "")
		return
	}
	if r.FormValue("new") != r.FormValue("new2") {
		back(w, r, "/admin/users", errors.New("las contraseñas nuevas no coinciden"), "")
		return
	}
	err := a.users.SetPassword(me, r.FormValue("new"))
	if err == nil {
		c, _ := r.Cookie(sessionCookie)
		a.sessions.dropUser(me, c.Value)
	}
	back(w, r, "/admin/users", err, "Contraseña cambiada")
}

func (a *App) usersDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == currentUser(r) {
		back(w, r, "/admin/users", errors.New("no puedes borrar tu propio usuario"), "")
		return
	}
	err := a.users.Delete(name)
	if err == nil {
		a.sessions.dropUser(name, "")
	}
	back(w, r, "/admin/users", err, "Usuario borrado")
}
