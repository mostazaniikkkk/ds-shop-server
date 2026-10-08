# DS client ↔ store server protocol (v1)

Any server that implements these three endpoints can be a source for the
DS Shop client ([ds-shop](https://github.com/mostazaniikkkk/ds-shop)).
The reference server is
[ds-shop-server](https://github.com/mostazaniikkkk/ds-shop-server).

- **Plain HTTP/1.x** only: the original DS cannot do TLS, and its Wi‑Fi only
  supports open or WEP networks.
- All integers are **little‑endian**. Strings are **UTF‑8**.
- A source URL may include a path prefix (`http://host:8080/shop`); the
  endpoints live under that prefix.

## `GET /ds/v1/catalog?lang=N`

`N` is the DS firmware language (0 Japanese, 1 English, 2 French, 3 German,
4 Italian, 5 Spanish); the server uses it to pick the banner title. The
response is binary (`application/octet-stream`):

```
char[4]  "DSSC"
u8       version (1)
u8       language used
u16      reserved (0)
u32      catalog revision (increases with every change)
str16    store name
str16    welcome message (may contain '\n')
u16      number of categories
  u16    category id (≠ 0)
  str8   name
u16      number of titles (published ones only)
  u32    title id
  u16    category id (0 = uncategorized)
  u32    size in bytes
  u32    date added (Unix time)
  char[4] game code (zero-padded)
  u8     flags: bit0 = has icon, bit1 = has description
  u8     number of title lines (1..3)
    str8 line
  str8   file name used when saving to the SD card
  [if bit0] u8[512] icon (4×4 tiles of 8×8, 4bpp, NDS banner format)
            u16[16] BGR555 palette
```

`str8` = `u8` length + bytes; `str16` = `u16` length + bytes.
The client accepts catalogs of up to 512 KB and shows up to 512 titles.

## `GET /ds/v1/title/{id}/desc`

Title description as UTF‑8 plain text (`\n` for line breaks).
The client reads at most 511 bytes.

## `GET /ds/v1/title/{id}/file`

The ROM. It must include `Content-Length`; the client checks that exactly that
many bytes arrive and that they match the catalog size before renaming the
`.part` file to its final name.

The client downloads in chunks with `Range: bytes=N-M` and, to resume an
interrupted download, continues from where it stopped; it expects
`206 Partial Content`. If the server answers `200`, the client starts over
from scratch, so `Range` is optional but recommended (the reference server
uses `http.ServeContent`). Requests whose range does not start at byte 0 are
not counted as new downloads in the statistics.

Hidden or nonexistent titles return `404`.

In the reference server, titles that are **direct links** are served the same
way: the server requests the file (or range) from the external URL and relays
it. If the external file no longer matches the catalog size, it returns `502`.
