package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Linked titles: the file lives on another server (GitHub, for example).
// The DS cannot do HTTPS, so the store server fetches it from the
// other server and passes it through as is, with Range support.

var errLinkChanged = errors.New("el archivo enlazado ha cambiado de tamano: comprueba el enlace en el panel")

// ValidateURL accepts only absolute http(s) links.
func ValidateURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("el enlace tiene que empezar por http:// o https://")
	}
	return u.String(), nil
}

// rangeReader reads chunks of a URL with "Range" requests. If the server does
// not support Range, it reports errNoRange.
type rangeReader struct {
	ctx    context.Context
	client *http.Client
	url    string
}

var errNoRange = errors.New("el servidor no admite descargas por partes (Range)")

func (r *rangeReader) ReadAt(p []byte, off int64) (int, error) {
	req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+int64(len(p))-1))
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusOK:
		return 0, errNoRange
	default:
		return 0, fmt.Errorf("el servidor respondio %s", resp.Status)
	}
	n, err := io.ReadFull(resp.Body, p)
	if err == io.ErrUnexpectedEOF {
		err = io.EOF
	}
	return n, err
}

// remoteSize requests the first byte to learn the total size (Content-Range).
func remoteSize(ctx context.Context, client *http.Client, raw string) (int64, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := client.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
		total := totalFromContentRange(resp.Header.Get("Content-Range"))
		if total <= 0 {
			return 0, false, errors.New("el servidor no indica el tamano del archivo")
		}
		return total, true, nil
	case http.StatusOK:
		if resp.ContentLength <= 0 {
			return 0, false, errors.New("el servidor no indica el tamano del archivo")
		}
		return resp.ContentLength, false, nil
	default:
		return 0, false, fmt.Errorf("el servidor respondio %s", resp.Status)
	}
}

func totalFromContentRange(cr string) int64 {
	i := strings.LastIndexByte(cr, '/')
	if i < 0 {
		return -1
	}
	n, err := strconv.ParseInt(cr[i+1:], 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// prefixReader serves ReadAt over the first bytes of a normal download
// (for servers without Range): reads sequentially as much as needed.
type prefixReader struct {
	body io.Reader
	buf  []byte
	max  int
}

func (p *prefixReader) ReadAt(b []byte, off int64) (int, error) {
	need := int(off) + len(b)
	if need > p.max {
		return 0, errors.New("el banner esta demasiado lejos del principio del archivo")
	}
	for len(p.buf) < need {
		chunk := make([]byte, 64*1024)
		n, err := p.body.Read(chunk)
		p.buf = append(p.buf, chunk[:n]...)
		if err != nil {
			if len(p.buf) < need {
				return 0, io.EOF
			}
			break
		}
	}
	return copy(b, p.buf[off:need]), nil
}

// ProbeURL checks a link and reads the ROM header and banner.
func ProbeURL(ctx context.Context, client *http.Client, raw string) (*Banner, int64, error) {
	size, ranges, err := remoteSize(ctx, client, raw)
	if err != nil {
		return nil, 0, err
	}
	if ranges {
		ban, err := ReadBanner(&rangeReader{ctx: ctx, client: client, url: raw}, size)
		return ban, size, err
	}
	// no Range: download from the start until the banner is reached
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	ban, err := ReadBanner(&prefixReader{body: resp.Body, max: 64 << 20}, size)
	return ban, size, err
}

// parseRange parses "bytes=a-b" / "bytes=a-" (a single range).
func parseRange(h string, size int64) (start, end int64, ok bool) {
	if h == "" {
		return 0, size - 1, true
	}
	spec, found := strings.CutPrefix(h, "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, 0, false
	}
	a, b, _ := strings.Cut(spec, "-")
	var err error
	if a == "" { // suffix: the last b bytes
		n, err := strconv.ParseInt(b, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		if n > size {
			n = size
		}
		return size - n, size - 1, true
	}
	if start, err = strconv.ParseInt(a, 10, 64); err != nil || start < 0 || start >= size {
		return 0, 0, false
	}
	end = size - 1
	if b != "" {
		if end, err = strconv.ParseInt(b, 10, 64); err != nil || end < start {
			return 0, 0, false
		}
		if end >= size {
			end = size - 1
		}
	}
	return start, end, true
}

// proxyLink sends the linked file to the DS (or the chunk it requests).
func (a *App) proxyLink(w http.ResponseWriter, r *http.Request, t *Title) {
	rg := r.Header.Get("Range")
	start, end, ok := parseRange(rg, t.Size)
	if !ok {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", t.Size))
		http.Error(w, "rango no valido", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, t.URL, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if rg != "" {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	}
	resp, err := a.client.Do(req)
	if err != nil {
		log.Printf("enlace %d: %v", t.ID, err)
		http.Error(w, "no se ha podido descargar el archivo enlazado", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	n := end - start + 1
	switch {
	case resp.StatusCode == http.StatusPartialContent:
		if total := totalFromContentRange(resp.Header.Get("Content-Range")); total != t.Size {
			a.linkChanged(w, t, total)
			return
		}
	case resp.StatusCode == http.StatusOK:
		if resp.ContentLength >= 0 && resp.ContentLength != t.Size {
			a.linkChanged(w, t, resp.ContentLength)
			return
		}
		// the other server does not support Range: skip the excess
		if start > 0 {
			if _, err := io.CopyN(io.Discard, resp.Body, start); err != nil {
				http.Error(w, "error leyendo el archivo enlazado", http.StatusBadGateway)
				return
			}
		}
	default:
		log.Printf("enlace %d: el servidor respondio %s", t.ID, resp.Status)
		http.Error(w, "el archivo enlazado no esta disponible", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
	if rg != "" {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, t.Size))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	if _, err := io.CopyN(w, resp.Body, n); err != nil {
		log.Printf("enlace %d: copia interrumpida: %v", t.ID, err)
	}
}

func (a *App) linkChanged(w http.ResponseWriter, t *Title, got int64) {
	log.Printf("enlace %d: tamano %d, esperado %d", t.ID, got, t.Size)
	_ = a.store.UpdateLink(t.ID, t.URL, nil, 0, errLinkChanged)
	http.Error(w, errLinkChanged.Error(), http.StatusBadGateway)
}

// newHTTPClient: client for external links (follows redirects, such as GitHub's).
func newHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{Transport: tr}
}
