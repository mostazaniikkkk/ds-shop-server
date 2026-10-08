package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"image/png"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"size": func(n int64) string {
		if n >= 1<<20 {
			return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
		}
		return fmt.Sprintf("%d KB", (n+1023)/1024)
	},
	"blocks": func(n int64) int64 { return (n + 131071) / 131072 },
	"name":   func(t *Title) string { return strings.Join(t.DisplayName(LangSpanish), " · ") },
	"first":  func(t *Title) string { return t.DisplayName(LangSpanish)[0] },
	"rest": func(t *Title) string {
		l := t.DisplayName(LangSpanish)
		return strings.Join(l[1:], " · ")
	},
	"date": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.Local().Format("02/01/2006 15:04")
	},
	"catname": func(cats []*Category, id uint16) string {
		for _, c := range cats {
			if c.ID == id {
				return c.Name
			}
		}
		return ""
	},
	// bannerRows groups the languages that share the same banner title
	"bannerRows": func(t *Title) [][2]string {
		names := []string{"Japonés", "Inglés", "Francés", "Alemán", "Italiano", "Español"}
		var rows [][2]string
		used := make([]bool, numLangs)
		for i := 0; i < numLangs; i++ {
			if used[i] || t.Banner[i] == "" {
				continue
			}
			var langs []string
			for j := i; j < numLangs; j++ {
				if !used[j] && t.Banner[j] == t.Banner[i] {
					used[j] = true
					langs = append(langs, names[j])
				}
			}
			label := strings.Join(langs, ", ")
			if len(langs) == numLangs {
				label = "Todos los idiomas"
			}
			rows = append(rows, [2]string{label, t.Banner[i]})
		}
		return rows
	},
}).ParseFS(webFS, "web/*.html"))

func (a *App) adminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/login", a.loginPage)
	mux.HandleFunc("POST /admin/login", a.loginPost)
	mux.HandleFunc("POST /admin/logout", a.logout)
	mux.HandleFunc("GET /admin/static/{file}", a.adminStatic)

	mux.HandleFunc("GET /admin/{$}", a.auth(a.adminIndex))
	mux.HandleFunc("GET /admin/icon/{id}", a.auth(a.adminIcon))
	mux.HandleFunc("POST /admin/upload", a.auth(a.adminUpload))
	mux.HandleFunc("POST /admin/link", a.auth(a.adminLink))
	mux.HandleFunc("GET /admin/title/{id}", a.auth(a.adminTitle))
	mux.HandleFunc("POST /admin/title/{id}", a.auth(a.adminTitleSave))
	mux.HandleFunc("POST /admin/title/{id}/delete", a.auth(a.adminTitleDelete))
	mux.HandleFunc("GET /admin/title/{id}/rom", a.auth(a.adminTitleROM))
	mux.HandleFunc("POST /admin/title/{id}/check", a.auth(a.adminTitleCheck))
	mux.HandleFunc("POST /admin/title/{id}/icon", a.auth(a.adminTitleIcon))
	mux.HandleFunc("POST /admin/title/{id}/icon/reset", a.auth(a.adminTitleIconReset))

	mux.HandleFunc("GET /admin/settings", a.auth(a.adminSettings))
	mux.HandleFunc("POST /admin/info", a.auth(a.adminInfo))
	mux.HandleFunc("POST /admin/category", a.auth(a.adminCategoryAdd))
	mux.HandleFunc("POST /admin/category/{id}", a.auth(a.adminCategoryEdit))

	mux.HandleFunc("GET /admin/users", a.auth(a.usersPage))
	mux.HandleFunc("POST /admin/users", a.auth(a.usersAdd))
	mux.HandleFunc("POST /admin/users/password", a.auth(a.usersPassword))
	mux.HandleFunc("POST /admin/users/{name}/delete", a.auth(a.usersDelete))
}

type page struct {
	D       storeData
	T       *Title
	User    string
	Section string
	Msg     string
	Err     string
	DSURLs  []string
	Users   []string
	// catalog summary
	Visible, Links, Broken int
	Total                  int64
}

func (a *App) render(w http.ResponseWriter, r *http.Request, name string, p page) {
	p.Msg = r.URL.Query().Get("ok")
	p.Err = r.URL.Query().Get("err")
	p.User = currentUser(r)
	if p.D.Name == "" {
		p.D = a.store.Snapshot()
	}
	p.Section = strings.TrimSuffix(name, ".html")
	a.renderPlain(w, name, p)
}

func (a *App) renderPlain(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func back(w http.ResponseWriter, r *http.Request, to string, err error, ok string) {
	q := url.Values{}
	if err != nil {
		q.Set("err", err.Error())
	} else if ok != "" {
		q.Set("ok", ok)
	}
	if len(q) > 0 {
		to += "?" + q.Encode()
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func titleID(r *http.Request) uint32 {
	id, _ := strconv.ParseUint(r.PathValue("id"), 10, 32)
	return uint32(id)
}

func titlePath(id uint32) string { return fmt.Sprintf("/admin/title/%d", id) }

// ---------------------------------------------------------------- catalog

func (a *App) adminIndex(w http.ResponseWriter, r *http.Request) {
	p := page{D: a.store.Snapshot()}
	for _, t := range p.D.Titles {
		if t.Visible {
			p.Visible++
		}
		if t.IsLink() {
			p.Links++
			if t.LinkError != "" {
				p.Broken++
			}
		}
		p.Total += t.Downloads
	}
	a.render(w, r, "index.html", p)
}

func (a *App) adminStatic(w http.ResponseWriter, r *http.Request) {
	f := r.PathValue("file")
	b, err := webFS.ReadFile("web/" + f)
	if err != nil || strings.Contains(f, "/") {
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasSuffix(f, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case strings.HasSuffix(f, ".js"):
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case strings.HasSuffix(f, ".svg"):
		w.Header().Set("Content-Type", "image/svg+xml")
	default:
		http.NotFound(w, r)
		return
	}
	w.Write(b)
}

func (a *App) adminIcon(w http.ResponseWriter, r *http.Request) {
	t, ok := a.store.Title(titleID(r))
	if !ok || len(t.Icon) != IconSize {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "max-age=86400") // the URL carries ?v=<revision>
	png.Encode(w, IconImage(t.Icon))
}

func (a *App) adminUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<30)
	mr, err := r.MultipartReader()
	if err != nil {
		back(w, r, "/admin/", err, "")
		return
	}
	var cat uint16
	var added, failed []string
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		if part.FormName() == "category" {
			buf := make([]byte, 16)
			n, _ := part.Read(buf)
			v, _ := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
			cat = uint16(v)
			continue
		}
		if part.FileName() == "" {
			continue
		}
		t, err := a.store.AddROM(part.FileName(), part, cat)
		if err != nil {
			failed = append(failed, part.FileName()+": "+err.Error())
			continue
		}
		added = append(added, t.DisplayName(LangSpanish)[0])
	}
	if len(failed) > 0 {
		back(w, r, "/admin/", fmt.Errorf("%s", strings.Join(failed, "; ")), "")
		return
	}
	if len(added) == 0 {
		back(w, r, "/admin/", errors.New("no se ha subido ningún archivo"), "")
		return
	}
	back(w, r, "/admin/", nil, "Añadido: "+strings.Join(added, ", "))
}

// adminLink adds a direct-link title: reads the ROM header and banner
// from the URL (icon, titles, code and size).
func (a *App) adminLink(w http.ResponseWriter, r *http.Request) {
	raw, err := ValidateURL(r.FormValue("url"))
	if err != nil {
		back(w, r, "/admin/", err, "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	ban, size, err := ProbeURL(ctx, a.client, raw)
	if err != nil {
		back(w, r, "/admin/", fmt.Errorf("no se ha podido usar el enlace: %w", err), "")
		return
	}
	cat, _ := strconv.Atoi(r.FormValue("category"))
	t, err := a.store.AddLink(raw, r.FormValue("file_name"), ban, size, uint16(cat))
	if err != nil {
		back(w, r, "/admin/", err, "")
		return
	}
	back(w, r, titlePath(t.ID), nil, "Enlace añadido: "+t.DisplayName(LangSpanish)[0])
}

func (a *App) adminTitle(w http.ResponseWriter, r *http.Request) {
	t, ok := a.store.Title(titleID(r))
	if !ok {
		http.NotFound(w, r)
		return
	}
	a.render(w, r, "title.html", page{D: a.store.Snapshot(), T: t})
}

func (a *App) adminTitleSave(w http.ResponseWriter, r *http.Request) {
	id := titleID(r)
	cat, _ := strconv.Atoi(r.FormValue("category"))
	err := a.store.UpdateTitle(id, TitleUpdate{
		Name:        r.FormValue("name"),
		Description: r.FormValue("description"),
		FileName:    r.FormValue("file_name"),
		Category:    uint16(cat),
		Visible:     r.FormValue("visible") == "on",
	})
	back(w, r, titlePath(id), err, "Cambios guardados")
}

func (a *App) adminTitleDelete(w http.ResponseWriter, r *http.Request) {
	back(w, r, "/admin/", a.store.DeleteTitle(titleID(r)), "Título borrado")
}

func (a *App) adminTitleROM(w http.ResponseWriter, r *http.Request) {
	t, ok := a.store.Title(titleID(r))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if t.IsLink() {
		http.Redirect(w, r, t.URL, http.StatusFound)
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(t.FileName))
	http.ServeFile(w, r, a.store.RomPath(t))
}

// adminTitleCheck re-reads a link (optionally with a new URL).
func (a *App) adminTitleCheck(w http.ResponseWriter, r *http.Request) {
	id := titleID(r)
	t, ok := a.store.Title(id)
	if !ok || !t.IsLink() {
		http.NotFound(w, r)
		return
	}
	raw := t.URL
	if nu := strings.TrimSpace(r.FormValue("url")); nu != "" {
		var err error
		if raw, err = ValidateURL(nu); err != nil {
			back(w, r, titlePath(id), err, "")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	ban, size, err := ProbeURL(ctx, a.client, raw)
	if err != nil {
		if raw == t.URL {
			_ = a.store.UpdateLink(id, raw, nil, 0, err)
		}
		back(w, r, titlePath(id), fmt.Errorf("el enlace no funciona: %w", err), "")
		return
	}
	back(w, r, titlePath(id), a.store.UpdateLink(id, raw, ban, size, nil), "Enlace comprobado: funciona")
}

func (a *App) adminTitleIcon(w http.ResponseWriter, r *http.Request) {
	id := titleID(r)
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	f, _, err := r.FormFile("icon")
	if err != nil {
		back(w, r, titlePath(id), errors.New("elige una imagen"), "")
		return
	}
	defer f.Close()
	icon, err := MakeIcon(f)
	if err != nil {
		back(w, r, titlePath(id), err, "")
		return
	}
	back(w, r, titlePath(id), a.store.SetIcon(id, icon), "Icono cambiado")
}

func (a *App) adminTitleIconReset(w http.ResponseWriter, r *http.Request) {
	id := titleID(r)
	back(w, r, titlePath(id), a.store.SetIcon(id, nil), "Se ha vuelto al icono de la ROM")
}

// ---------------------------------------------------------------- store and categories

func (a *App) adminSettings(w http.ResponseWriter, r *http.Request) {
	p := page{D: a.store.Snapshot()}
	port := ""
	if _, pp, err := net.SplitHostPort(r.Host); err == nil && pp != "80" {
		port = ":" + pp
	}
	for _, ip := range localIPs() {
		p.DSURLs = append(p.DSURLs, "http://"+ip+port)
	}
	a.render(w, r, "settings.html", p)
}

func (a *App) adminInfo(w http.ResponseWriter, r *http.Request) {
	err := a.store.SetInfo(r.FormValue("name"), r.FormValue("message"))
	back(w, r, "/admin/settings", err, "Datos de la tienda guardados")
}

func (a *App) adminCategoryAdd(w http.ResponseWriter, r *http.Request) {
	err := a.store.AddCategory(r.FormValue("name"))
	back(w, r, "/admin/settings", err, "Categoría creada")
}

func (a *App) adminCategoryEdit(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseUint(r.PathValue("id"), 10, 16)
	if r.FormValue("action") == "delete" {
		back(w, r, "/admin/settings", a.store.DeleteCategory(uint16(id)), "Categoría borrada")
		return
	}
	back(w, r, "/admin/settings", a.store.RenameCategory(uint16(id), r.FormValue("name")), "Categoría renombrada")
}
