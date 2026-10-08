package main

import (
	"bytes"
	"encoding/binary"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// API for the Nintendo DS client. See PROTOCOL.md.
//
// Plain HTTP only: the original DS cannot do TLS in a reasonable time.

const catalogVersion = 1

func (a *App) dsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /ds/v1/catalog", a.dsCatalog)
	mux.HandleFunc("GET /ds/v1/title/{id}/desc", a.dsDesc)
	mux.HandleFunc("GET /ds/v1/title/{id}/file", a.dsFile)
}

type wbuf struct{ bytes.Buffer }

func (w *wbuf) u8(v uint8)   { w.WriteByte(v) }
func (w *wbuf) u16(v uint16) { binary.Write(w, binary.LittleEndian, v) }
func (w *wbuf) u32(v uint32) { binary.Write(w, binary.LittleEndian, v) }

// str8/str16: length-prefixed UTF-8 string (truncated without splitting characters)
func (w *wbuf) str8(s string)  { b := clip(s, 255); w.u8(uint8(len(b))); w.Write(b) }
func (w *wbuf) str16(s string) { b := clip(s, 4096); w.u16(uint16(len(b))); w.Write(b) }

func clip(s string, max int) []byte {
	b := []byte(s)
	if len(b) <= max {
		return b
	}
	b = b[:max]
	for len(b) > 0 && b[len(b)-1]&0xC0 == 0x80 {
		b = b[:len(b)-1]
	}
	if len(b) > 0 && b[len(b)-1] >= 0xC0 {
		b = b[:len(b)-1]
	}
	return b
}

func encodeCatalog(d storeData, lang int) []byte {
	var w wbuf
	w.WriteString("DSSC")
	w.u8(catalogVersion)
	w.u8(uint8(lang))
	w.u16(0)
	w.u32(d.Revision)
	w.str16(d.Name)
	w.str16(d.Message)

	w.u16(uint16(len(d.Categories)))
	for _, c := range d.Categories {
		w.u16(c.ID)
		w.str8(c.Name)
	}

	var visible []*Title
	for _, t := range d.Titles {
		if t.Visible {
			visible = append(visible, t)
		}
	}
	w.u16(uint16(len(visible)))
	for _, t := range visible {
		w.u32(t.ID)
		w.u16(t.Category)
		w.u32(uint32(t.Size))
		w.u32(uint32(t.Added.Unix()))
		gc := []byte(t.GameCode + "\x00\x00\x00\x00")[:4]
		w.Write(gc)
		var flags uint8
		if len(t.Icon) == IconSize {
			flags |= 1
		}
		if t.Description != "" {
			flags |= 2
		}
		w.u8(flags)
		lines := t.DisplayName(lang)
		w.u8(uint8(len(lines)))
		for _, l := range lines {
			w.str8(l)
		}
		w.str8(t.FileName)
		if flags&1 != 0 {
			w.Write(t.Icon)
		}
	}
	return w.Bytes()
}

func langParam(r *http.Request) int {
	l, err := strconv.Atoi(r.URL.Query().Get("lang"))
	if err != nil || l < 0 || l >= numLangs {
		return LangEnglish
	}
	return l
}

func (a *App) dsCatalog(w http.ResponseWriter, r *http.Request) {
	b := encodeCatalog(a.store.Snapshot(), langParam(r))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Write(b)
}

func (a *App) titleParam(w http.ResponseWriter, r *http.Request) (*Title, bool) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 32)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	t, ok := a.store.Title(uint32(id))
	if !ok || !t.Visible {
		http.NotFound(w, r)
		return nil, false
	}
	return t, true
}

func (a *App) dsDesc(w http.ResponseWriter, r *http.Request) {
	t, ok := a.titleParam(w, r)
	if !ok {
		return
	}
	b := []byte(t.Description)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Write(b)
}

func (a *App) dsFile(w http.ResponseWriter, r *http.Request) {
	t, ok := a.titleParam(w, r)
	if !ok {
		return
	}
	// A download counts when it starts from the beginning (the client requests the
	// ROM in chunks with Range; later chunks and resumes do not count)
	if rg := r.Header.Get("Range"); rg == "" || strings.HasPrefix(rg, "bytes=0-") {
		a.store.CountDownload(t.ID)
	}
	if t.IsLink() {
		a.proxyLink(w, r, t)
		return
	}
	f, err := os.Open(a.store.RomPath(t))
	if err != nil {
		http.Error(w, "archivo no disponible", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, t.FileName, st.ModTime(), f)
}
