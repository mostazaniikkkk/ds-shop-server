# DS Shop — store server

A Go server (standard library only, no external dependencies) that publishes
the catalog for the Nintendo DS client
[ds-shop](https://github.com/mostazaniikkkk/ds-shop). The API the DS uses
is documented in [PROTOCOL.md](PROTOCOL.md).

```
go build -o ds-shop-server .
./ds-shop-server -addr :8080 -data data -admin-pass a-password
```

| Flag | Default | Meaning |
|---|---|---|
| `-addr` | `:8080` | listen address |
| `-data` | `data` | data folder (catalog and ROMs) |
| `-admin-user` | `admin` | first admin user (only if there are no users yet) |
| `-admin-pass` | `$DSISHOP_ADMIN_PASS` | that user's password; if empty, one is generated and printed to the console |

The web admin panel is at `http://localhost:8080/admin/` and requires a user
name and password. On first run the `admin` user is created with the
`-admin-pass` password; after that, users are managed in the panel's
**Usuarios** (Users) page. The panel UI is currently in Spanish.

- **Catalog:** each title is either
  - an **uploaded file** (stored in `data/roms/`), or
  - a **direct link** to an `.nds` hosted elsewhere (for example a GitHub
    release). The server reads the ROM header and banner from the link (icon,
    title, game code and size) and, when the DS downloads it, fetches it from
    the other server and relays it as is (the DS cannot use HTTPS). Ranged
    downloads work even if the other server does not support them. If the
    linked file changes size, the DS gets an error and the panel flags it;
    "Comprobar enlace" (Check link) refreshes it.
- Each title has a name, description, category, file name on the SD card,
  published/hidden state and an **icon**: the banner's own, or a custom one
  (PNG/GIF/JPG, converted to 32×32 with 15 colors).
- **Store settings:** name, welcome message, categories, and the addresses to
  enter on the DS.
- Everything is stored in the `-data` folder: `store.json`, `users.json` and
  `roms/`.
- `go test ./...` covers the catalog, downloads (local and linked, with and
  without `Range`), login, users and icon conversion.
- For a DS on your network to connect, the firewall must allow incoming
  connections on the port. On Windows, as administrator:
  `netsh advfirewall firewall add rule name="DS Shop" dir=in action=allow protocol=TCP localport=8080`
