package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// fakeROM builds a minimal ROM with header and banner.
func fakeROM(code, title string) []byte {
	rom := make([]byte, 0x1000)
	copy(rom[0x0C:], code)
	binary.LittleEndian.PutUint16(rom[0x15C:], 0xCF56)
	binary.LittleEndian.PutUint32(rom[0x68:], 0x400)
	ban := rom[0x400:]
	binary.LittleEndian.PutUint16(ban, 1)
	for i := 0; i < 512; i++ {
		ban[0x20+i] = 0x11
	}
	binary.LittleEndian.PutUint16(ban[0x222:], 0x7FFF)
	u := utf16.Encode([]rune(title))
	for l := 0; l < numLangs; l++ {
		for i, c := range u {
			binary.LittleEndian.PutUint16(ban[0x240+l*0x100+i*2:], c)
		}
	}
	return rom
}

type rd struct {
	b []byte
	p int
}

func (r *rd) u8() uint8   { v := r.b[r.p]; r.p++; return v }
func (r *rd) u16() uint16 { v := binary.LittleEndian.Uint16(r.b[r.p:]); r.p += 2; return v }
func (r *rd) u32() uint32 { v := binary.LittleEndian.Uint32(r.b[r.p:]); r.p += 4; return v }
func (r *rd) s8() string  { n := int(r.u8()); s := string(r.b[r.p : r.p+n]); r.p += n; return s }
func (r *rd) s16() string { n := int(r.u16()); s := string(r.b[r.p : r.p+n]); r.p += n; return s }

func newTestApp(t *testing.T) (*App, http.Handler) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	users, err := OpenUsers(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := users.SetPassword("admin", "secreto1"); err != nil {
		t.Fatal(err)
	}
	a := &App{store: st, users: users, client: newHTTPClient()}
	mux := http.NewServeMux()
	a.dsRoutes(mux)
	a.adminRoutes(mux)
	return a, mux
}

func TestCatalogRoundTrip(t *testing.T) {
	a, h := newTestApp(t)
	if err := a.store.AddCategory("Juegos"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.AddROM("juego.nds", bytes.NewReader(fakeROM("ABCE", "Mi Juego\nEstudio")), 1); err != nil {
		t.Fatal(err)
	}
	hid, err := a.store.AddROM("oculto.nds", bytes.NewReader(fakeROM("XYZE", "Oculto")), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.UpdateTitle(hid.ID, TitleUpdate{FileName: "oculto.nds", Visible: false}); err != nil {
		t.Fatal(err)
	}
	if err := a.store.SetInfo("Mi tienda", "Hola"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/ds/v1/catalog?lang=5", nil))
	if rec.Code != 200 {
		t.Fatalf("catalog: %d", rec.Code)
	}
	r := &rd{b: rec.Body.Bytes()}
	if string(r.b[:4]) != "DSSC" {
		t.Fatal("magic")
	}
	r.p = 4
	if v := r.u8(); v != catalogVersion {
		t.Fatalf("version %d", v)
	}
	if l := r.u8(); l != 5 {
		t.Fatalf("lang %d", l)
	}
	r.u16()
	r.u32()
	if n := r.s16(); n != "Mi tienda" {
		t.Fatalf("name %q", n)
	}
	if m := r.s16(); m != "Hola" {
		t.Fatalf("msg %q", m)
	}
	if nc := r.u16(); nc != 1 {
		t.Fatalf("cats %d", nc)
	}
	if id := r.u16(); id != 1 || r.s8() != "Juegos" {
		t.Fatal("category")
	}
	if nt := r.u16(); nt != 1 {
		t.Fatalf("solo debe salir el titulo visible, hay %d", nt)
	}
	id := r.u32()
	if cat := r.u16(); cat != 1 {
		t.Fatalf("cat %d", cat)
	}
	if size := r.u32(); size != 0x1000 {
		t.Fatalf("size %d", size)
	}
	r.u32()
	if gc := string(r.b[r.p : r.p+4]); gc != "ABCE" {
		t.Fatalf("gamecode %q", gc)
	}
	r.p += 4
	if f := r.u8(); f != 1 {
		t.Fatalf("flags %d", f)
	}
	if nl := r.u8(); nl != 2 || r.s8() != "Mi Juego" || r.s8() != "Estudio" {
		t.Fatal("lineas del titulo")
	}
	if fn := r.s8(); fn != "juego.nds" {
		t.Fatalf("file %q", fn)
	}
	r.p += IconSize
	if r.p != len(r.b) {
		t.Fatalf("sobran %d bytes", len(r.b)-r.p)
	}

	// full and partial download
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/ds/v1/title/1/file", nil))
	if rec.Code != 200 || rec.Body.Len() != 0x1000 {
		t.Fatalf("file: %d %d", rec.Code, rec.Body.Len())
	}
	req := httptest.NewRequest("GET", "/ds/v1/title/1/file", nil)
	req.Header.Set("Range", "bytes=16-")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 206 || rec.Body.Len() != 0x1000-16 {
		t.Fatalf("range: %d %d", rec.Code, rec.Body.Len())
	}
	if tt, _ := a.store.Title(id); tt.Downloads != 1 {
		t.Fatalf("descargas %d", tt.Downloads)
	}
	// hidden ones are not served
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/ds/v1/title/2/file", nil))
	if rec.Code != 404 {
		t.Fatalf("oculto: %d", rec.Code)
	}
}

func TestRejectsNonROM(t *testing.T) {
	a, _ := newTestApp(t)
	if _, err := a.store.AddROM("x.nds", strings.NewReader("hola"), 0); err == nil {
		t.Fatal("deberia rechazar archivos que no son ROMs")
	}
}

// login returns the session cookie
func login(t *testing.T, h http.Handler, user, pass string) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest("POST", "/admin/login", strings.NewReader("user="+user+"&pass="+pass))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c
		}
	}
	return nil
}

func TestAdminAuth(t *testing.T) {
	_, h := newTestApp(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/", nil))
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/admin/login") {
		t.Fatalf("sin sesion deberia ir al login: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if c := login(t, h, "admin", "mala"); c != nil {
		t.Fatal("contrasena incorrecta aceptada")
	}
	c := login(t, h, "admin", "secreto1")
	if c == nil {
		t.Fatal("no se ha podido entrar")
	}
	for _, path := range []string{"/admin/", "/admin/settings", "/admin/users"} {
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(c)
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != 200 || !bytes.Contains(body, []byte("Tienda DS")) {
			t.Fatalf("%s con sesion: %d %s", path, rec.Code, body)
		}
	}
	req := httptest.NewRequest("POST", "/admin/category", strings.NewReader("name=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://evil.example")
	req.AddCookie(c)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("csrf: %d", rec.Code)
	}
	// after logout the cookie is no longer valid
	req = httptest.NewRequest("POST", "/admin/logout", nil)
	req.AddCookie(c)
	h.ServeHTTP(httptest.NewRecorder(), req)
	req = httptest.NewRequest("GET", "/admin/", nil)
	req.AddCookie(c)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("tras salir: %d", rec.Code)
	}
}

// upstream simulates an external server (GitHub-like) with or without Range.
func upstream(t *testing.T, data []byte, ranges bool) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ranges {
			http.ServeContent(w, r, "x.nds", time.Time{}, bytes.NewReader(data))
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLinkedTitle(t *testing.T) {
	rom := fakeROM("LNKE", "Enlazado\nAutor")
	for _, ranges := range []bool{true, false} {
		a, h := newTestApp(t)
		up := upstream(t, rom, ranges)
		ban, size, err := ProbeURL(context.Background(), a.client, up.URL+"/releases/juego.nds")
		if err != nil {
			t.Fatalf("ranges=%v: %v", ranges, err)
		}
		if size != int64(len(rom)) || ban.GameCode != "LNKE" || ban.Titles[LangSpanish] != "Enlazado\nAutor" {
			t.Fatalf("ranges=%v: datos %d %+v", ranges, size, ban)
		}
		tt, err := a.store.AddLink(up.URL+"/releases/juego.nds", "", ban, size, 0)
		if err != nil {
			t.Fatal(err)
		}
		if tt.FileName != "juego.nds" {
			t.Fatalf("nombre %q", tt.FileName)
		}
		path := "/ds/v1/title/" + strconv.Itoa(int(tt.ID)) + "/file"
		// full
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), rom) {
			t.Fatalf("ranges=%v completo: %d %d", ranges, rec.Code, rec.Body.Len())
		}
		// one chunk, the way the DS requests them
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Range", "bytes=100-299")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 206 || !bytes.Equal(rec.Body.Bytes(), rom[100:300]) || rec.Header().Get("Content-Length") != "200" {
			t.Fatalf("ranges=%v tramo: %d %d", ranges, rec.Code, rec.Body.Len())
		}
		if got, _ := a.store.Title(tt.ID); got.Downloads != 1 {
			t.Fatalf("descargas %d", got.Downloads)
		}
	}
}

func TestLinkChangedSize(t *testing.T) {
	a, h := newTestApp(t)
	rom := fakeROM("LNKE", "Enlazado")
	up := upstream(t, rom, true)
	ban, _, err := ProbeURL(context.Background(), a.client, up.URL+"/j.nds")
	if err != nil {
		t.Fatal(err)
	}
	tt, _ := a.store.AddLink(up.URL+"/j.nds", "", ban, int64(len(rom))+5, 0) // size that no longer matches
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/ds/v1/title/"+strconv.Itoa(int(tt.ID))+"/file", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("deberia avisar del cambio: %d", rec.Code)
	}
	if got, _ := a.store.Title(tt.ID); got.LinkError == "" {
		t.Fatal("el error deberia quedar guardado para el panel")
	}
}

func TestMakeIcon(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.NRGBA{uint8(x * 4), uint8(y * 5), 200, 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	icon, err := MakeIcon(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(icon) != IconSize {
		t.Fatalf("tamano %d", len(icon))
	}
	// the image is landscape: the first rows stay transparent (index 0)
	// and the middle ones use palette colors
	if icon[0] != 0 || icon[(1*4+0)*32+4*4] == 0 {
		t.Fatalf("relleno o contenido inesperado")
	}
	back := IconImage(icon)
	if back.At(16, 16).(color.NRGBA).A != 255 || back.At(16, 1).(color.NRGBA).A != 0 {
		t.Fatal("el icono convertido no se ve como se esperaba")
	}
}

func TestParseRange(t *testing.T) {
	for _, c := range []struct {
		h          string
		start, end int64
		ok         bool
	}{
		{"", 0, 999, true}, {"bytes=0-", 0, 999, true}, {"bytes=10-19", 10, 19, true},
		{"bytes=990-5000", 990, 999, true}, {"bytes=-10", 990, 999, true},
		{"bytes=1000-", 0, 0, false}, {"bytes=5-1", 0, 0, false}, {"items=0-1", 0, 0, false},
	} {
		s, e, ok := parseRange(c.h, 1000)
		if ok != c.ok || (ok && (s != c.start || e != c.end)) {
			t.Errorf("%q: %d %d %v", c.h, s, e, ok)
		}
	}
}

func TestUsers(t *testing.T) {
	u, _ := OpenUsers(t.TempDir())
	if err := u.SetPassword("ana", "123"); err == nil {
		t.Fatal("contrasena corta aceptada")
	}
	u.SetPassword("ana", "contrasena")
	if !u.Check("ana", "contrasena") || u.Check("ana", "otra") || u.Check("nadie", "contrasena") {
		t.Fatal("comprobacion de contrasenas")
	}
	if err := u.Delete("ana"); err == nil {
		t.Fatal("no se puede borrar el ultimo usuario")
	}
}

func TestSanitizeFileName(t *testing.T) {
	for in, want := range map[string]string{
		"../../etc/passwd":  "passwd",
		`C:\juegos\a?b.nds`: "a_b.nds",
		"ñandú.nds":         "ñandú.nds",
	} {
		if got := sanitizeFileName(in); got != want {
			t.Errorf("%q: %q, quiero %q", in, got, want)
		}
	}
}
