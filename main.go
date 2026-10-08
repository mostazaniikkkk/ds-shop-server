// DS Store server: manages the catalog and serves it to DS consoles.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

type App struct {
	store    *Store
	users    *Users
	sessions sessions
	client   *http.Client // for linked titles
}

func main() {
	addr := flag.String("addr", ":8080", "direccion de escucha")
	data := flag.String("data", "data", "carpeta de datos (catalogo y ROMs)")
	user := flag.String("admin-user", "admin", "primer usuario del panel (si aun no hay ninguno)")
	pass := flag.String("admin-pass", os.Getenv("DSISHOP_ADMIN_PASS"), "su contrasena (o DSISHOP_ADMIN_PASS); vacia = se genera una")
	flag.Parse()

	st, err := OpenStore(*data)
	if err != nil {
		log.Fatal(err)
	}
	users, err := OpenUsers(*data)
	if err != nil {
		log.Fatal(err)
	}
	if users.Count() == 0 {
		p := *pass
		if p == "" {
			b := make([]byte, 6)
			rand.Read(b)
			p = hex.EncodeToString(b)
		}
		if err := users.SetPassword(*user, p); err != nil {
			log.Fatal(err)
		}
		log.Printf("creado el usuario del panel %q con contrasena %q (cambiala en Usuarios)", *user, p)
	}
	a := &App{store: st, users: users, client: newHTTPClient()}

	mux := http.NewServeMux()
	a.dsRoutes(mux)
	a.adminRoutes(mux)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           logRequests(mux),
		ReadHeaderTimeout: 15 * time.Second,
	}
	log.Printf("Tienda DS escuchando en %s (panel: http://localhost%s/admin/)", *addr, portOf(*addr))
	for _, ip := range localIPs() {
		log.Printf("  URL para la DS: http://%s%s", ip, portOf(*addr))
	}
	log.Fatal(srv.ListenAndServe())
}

func portOf(addr string) string {
	_, p, err := net.SplitHostPort(addr)
	if err != nil || p == "80" {
		return ""
	}
	return ":" + p
}

func localIPs() []string {
	var out []string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			out = append(out, n.IP.String())
		}
	}
	return out
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{w, 200}
		t := time.Now()
		h.ServeHTTP(sw, r)
		log.Printf("%s %s %s %d %s", r.RemoteAddr, r.Method, r.URL.RequestURI(), sw.code, time.Since(t).Round(time.Millisecond))
	})
}
