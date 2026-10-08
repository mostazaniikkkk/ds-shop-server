package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Category struct {
	ID   uint16 `json:"id"`
	Name string `json:"name"`
}

// Source of a title's file
const (
	SourceFile = "file" // uploaded to the server (data/roms/<id>.nds)
	SourceURL  = "url"  // external direct link; the server acts as a proxy
)

type Title struct {
	ID          uint32           `json:"id"`
	Source      string           `json:"source,omitempty"` // SourceFile (or empty) or SourceURL
	URL         string           `json:"url,omitempty"`    // if Source == SourceURL
	FileName    string           `json:"file_name"`        // name it is saved under on the DS
	Size        int64            `json:"size"`
	SHA1        string           `json:"sha1,omitempty"`
	GameCode    string           `json:"game_code"`
	Banner      [numLangs]string `json:"banner"`
	Icon        []byte           `json:"icon,omitempty"`        // the one the DS sees
	BannerIcon  []byte           `json:"banner_icon,omitempty"` // the one from the ROM banner
	IconCustom  bool             `json:"icon_custom,omitempty"` // Icon was uploaded by the admin
	Checked     time.Time        `json:"checked,omitempty"`     // last link check
	LinkError   string           `json:"link_error,omitempty"`  // last error when checking it
	Name        string           `json:"name,omitempty"`        // replaces the banner title if not empty
	Description string           `json:"description,omitempty"`
	Category    uint16           `json:"category"` // 0 = uncategorized
	Visible     bool             `json:"visible"`
	Added       time.Time        `json:"added"`
	Downloads   int64            `json:"downloads"`
}

func (t *Title) IsLink() bool { return t.Source == SourceURL }

// LinkHost returns the link's host (to show it in the panel).
func (t *Title) LinkHost() string {
	if u, err := url.Parse(t.URL); err == nil {
		return u.Host
	}
	return ""
}

// DisplayName returns the title lines (as in the banner: up to 3).
func (t *Title) DisplayName(lang int) []string {
	s := t.Name
	if s == "" && lang >= 0 && lang < numLangs {
		s = t.Banner[lang]
	}
	if s == "" {
		s = t.Banner[LangEnglish]
	}
	if s == "" {
		s = strings.TrimSuffix(t.FileName, filepath.Ext(t.FileName))
	}
	lines := strings.Split(strings.ReplaceAll(s, "\r", ""), "\n")
	if len(lines) > 3 {
		lines = lines[:3]
	}
	return lines
}

type storeData struct {
	Name       string      `json:"name"`
	Message    string      `json:"message"`
	Revision   uint32      `json:"revision"`
	NextID     uint32      `json:"next_id"`
	NextCat    uint16      `json:"next_category"`
	Categories []*Category `json:"categories"`
	Titles     []*Title    `json:"titles"`
}

// Store keeps the catalog in dir/store.json and the ROMs in dir/roms/.
type Store struct {
	mu  sync.RWMutex
	dir string
	d   storeData
}

func OpenStore(dir string) (*Store, error) {
	s := &Store{dir: dir}
	if err := os.MkdirAll(filepath.Join(dir, "roms"), 0o755); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(s.dbPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
		s.d = storeData{Name: "Tienda DS", Message: "", NextID: 1, NextCat: 1}
		return s, s.saveLocked()
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(b, &s.d); err != nil {
		return nil, fmt.Errorf("store.json: %w", err)
	}
	for _, t := range s.d.Titles { // data from previous versions
		if t.Source == "" {
			t.Source = SourceFile
		}
		if t.BannerIcon == nil && !t.IconCustom {
			t.BannerIcon = t.Icon
		}
	}
	return s, nil
}

func (s *Store) dbPath() string { return filepath.Join(s.dir, "store.json") }

func (s *Store) romPath(t *Title) string {
	return filepath.Join(s.dir, "roms", fmt.Sprintf("%d.nds", t.ID))
}

// saveLocked writes store.json atomically. The lock must be held.
func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(&s.d, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.dbPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.dbPath())
}

func (s *Store) changed() error {
	s.d.Revision++
	return s.saveLocked()
}

// Snapshot returns a copy of the catalog to read without the lock.
func (s *Store) Snapshot() storeData {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d := s.d
	d.Categories = make([]*Category, len(s.d.Categories))
	for i, c := range s.d.Categories {
		cc := *c
		d.Categories[i] = &cc
	}
	d.Titles = make([]*Title, len(s.d.Titles))
	for i, t := range s.d.Titles {
		tt := *t
		d.Titles[i] = &tt
	}
	sort.Slice(d.Titles, func(i, j int) bool {
		return strings.ToLower(d.Titles[i].DisplayName(LangEnglish)[0]) < strings.ToLower(d.Titles[j].DisplayName(LangEnglish)[0])
	})
	return d
}

func (s *Store) Title(id uint32) (*Title, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.d.Titles {
		if t.ID == id {
			tt := *t
			return &tt, true
		}
	}
	return nil, false
}

func (s *Store) RomPath(t *Title) string { return s.romPath(t) }

// AddROM copies a ROM into storage and adds it to the catalog.
func (s *Store) AddROM(fileName string, r io.Reader, category uint16) (*Title, error) {
	fileName = sanitizeFileName(fileName)
	if !strings.EqualFold(filepath.Ext(fileName), ".nds") {
		fileName += ".nds"
	}
	tmp, err := os.CreateTemp(filepath.Join(s.dir, "roms"), "upload-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	h := sha1.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		tmp.Close()
		return nil, err
	}
	ban, err := ReadBanner(tmp, size)
	if err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	t := &Title{
		ID:         s.d.NextID,
		Source:     SourceFile,
		FileName:   fileName,
		Size:       size,
		SHA1:       hex.EncodeToString(h.Sum(nil)),
		GameCode:   ban.GameCode,
		Banner:     ban.Titles,
		Icon:       ban.Icon,
		BannerIcon: ban.Icon,
		Category:   category,
		Visible:    true,
		Added:      time.Now().UTC(),
	}
	if s.categoryLocked(category) == nil {
		t.Category = 0
	}
	if err := os.Rename(tmp.Name(), s.romPath(t)); err != nil {
		return nil, err
	}
	s.d.NextID++
	s.d.Titles = append(s.d.Titles, t)
	tt := *t
	return &tt, s.changed()
}

// AddLink adds a title whose file is at an external URL. ban and size
// come from ProbeURL.
func (s *Store) AddLink(rawURL, fileName string, ban *Banner, size int64, category uint16) (*Title, error) {
	fileName = linkFileName(rawURL, fileName)
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &Title{
		ID:         s.d.NextID,
		Source:     SourceURL,
		URL:        rawURL,
		FileName:   fileName,
		Size:       size,
		GameCode:   ban.GameCode,
		Banner:     ban.Titles,
		Icon:       ban.Icon,
		BannerIcon: ban.Icon,
		Category:   category,
		Visible:    true,
		Added:      time.Now().UTC(),
		Checked:    time.Now().UTC(),
	}
	if s.categoryLocked(category) == nil {
		t.Category = 0
	}
	s.d.NextID++
	s.d.Titles = append(s.d.Titles, t)
	tt := *t
	return &tt, s.changed()
}

// linkFileName picks the file name on the SD: the given one or the end of the URL.
func linkFileName(rawURL, fileName string) string {
	if fileName = sanitizeFileName(fileName); fileName == "" {
		if u, err := url.Parse(rawURL); err == nil {
			fileName = sanitizeFileName(path.Base(u.Path))
		}
	}
	if fileName == "" || fileName == "." {
		fileName = "juego.nds"
	}
	if !strings.EqualFold(filepath.Ext(fileName), ".nds") {
		fileName += ".nds"
	}
	return fileName
}

// UpdateLink stores the result of checking a link (or the error).
func (s *Store) UpdateLink(id uint32, rawURL string, ban *Banner, size int64, probeErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.titleLocked(id)
	if t == nil || !t.IsLink() {
		return os.ErrNotExist
	}
	t.Checked = time.Now().UTC()
	if probeErr != nil {
		t.LinkError = probeErr.Error()
		return s.saveLocked()
	}
	t.LinkError = ""
	t.URL = rawURL
	t.Size = size
	t.GameCode = ban.GameCode
	t.Banner = ban.Titles
	t.BannerIcon = ban.Icon
	if !t.IconCustom {
		t.Icon = ban.Icon
	}
	return s.changed()
}

// SetIcon sets a custom icon (icon = nil reverts to the banner's).
func (s *Store) SetIcon(id uint32, icon []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.titleLocked(id)
	if t == nil {
		return os.ErrNotExist
	}
	if icon == nil {
		t.Icon, t.IconCustom = t.BannerIcon, false
	} else {
		t.Icon, t.IconCustom = icon, true
	}
	return s.changed()
}

func (s *Store) titleLocked(id uint32) *Title {
	for _, t := range s.d.Titles {
		if t.ID == id {
			return t
		}
	}
	return nil
}

type TitleUpdate struct {
	Name        string
	Description string
	FileName    string
	Category    uint16
	Visible     bool
}

func (s *Store) UpdateTitle(id uint32, u TitleUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.d.Titles {
		if t.ID != id {
			continue
		}
		t.Name = strings.TrimSpace(u.Name)
		t.Description = strings.TrimSpace(strings.ReplaceAll(u.Description, "\r", ""))
		if fn := sanitizeFileName(u.FileName); fn != "" {
			t.FileName = fn
		}
		if s.categoryLocked(u.Category) != nil {
			t.Category = u.Category
		} else {
			t.Category = 0
		}
		t.Visible = u.Visible
		return s.changed()
	}
	return os.ErrNotExist
}

func (s *Store) DeleteTitle(id uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.d.Titles {
		if t.ID == id {
			if !t.IsLink() {
				if err := os.Remove(s.romPath(t)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			s.d.Titles = append(s.d.Titles[:i], s.d.Titles[i+1:]...)
			return s.changed()
		}
	}
	return os.ErrNotExist
}

func (s *Store) CountDownload(id uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.d.Titles {
		if t.ID == id {
			t.Downloads++
			// downloads do not change the catalog: the revision is not bumped
			_ = s.saveLocked()
			return
		}
	}
}

func (s *Store) SetInfo(name, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Name = strings.TrimSpace(name)
	s.d.Message = strings.TrimSpace(strings.ReplaceAll(message, "\r", ""))
	return s.changed()
}

func (s *Store) categoryLocked(id uint16) *Category {
	for _, c := range s.d.Categories {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (s *Store) AddCategory(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("el nombre no puede estar vacio")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Categories = append(s.d.Categories, &Category{ID: s.d.NextCat, Name: name})
	s.d.NextCat++
	return s.changed()
}

func (s *Store) RenameCategory(id uint16, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("el nombre no puede estar vacio")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.categoryLocked(id)
	if c == nil {
		return os.ErrNotExist
	}
	c.Name = name
	return s.changed()
}

// DeleteCategory deletes the category; its titles become uncategorized.
func (s *Store) DeleteCategory(id uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.d.Categories {
		if c.ID == id {
			s.d.Categories = append(s.d.Categories[:i], s.d.Categories[i+1:]...)
			for _, t := range s.d.Titles {
				if t.Category == id {
					t.Category = 0
				}
			}
			return s.changed()
		}
	}
	return os.ErrNotExist
}

// sanitizeFileName produces a FAT-valid name with no paths.
func sanitizeFileName(n string) string {
	n = filepath.Base(strings.ReplaceAll(n, "\\", "/"))
	if n == "." || n == "/" {
		return ""
	}
	var b strings.Builder
	for _, r := range n {
		if r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r) {
			r = '_'
		}
		b.WriteRune(r)
	}
	s := strings.TrimSpace(b.String())
	if len(s) > 60 {
		ext := filepath.Ext(s)
		s = strings.ToValidUTF8(s[:60-len(ext)], "") + ext
	}
	return s
}
