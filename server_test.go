package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestParseID(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		{"  dQw4w9WgXcQ\n", "dQw4w9WgXcQ"},
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		// A video watched inside a Mix: the playlist must not come along.
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=RDdQ&index=7&t=95s", "dQw4w9WgXcQ"},
		{"https://youtu.be/aBcDeFgHiJ_?si=xyz", "aBcDeFgHiJ_"},
		{"https://www.youtube.com/shorts/1234567890a", "1234567890a"},
		{"https://music.youtube.com/watch?v=xyz-ABC_123", "xyz-ABC_123"},
		{"https://www.youtube.com/live/LiveStream1", "LiveStream1"},
		{"https://example.com/nope", ""},
		{"", ""},
		{"tooshort", ""},
	} {
		if got := parseID(tc.in); got != tc.want {
			t.Errorf("parseID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFileIDRoundTrip(t *testing.T) {
	// block() has to recover the id from a name produced by outTemplate.
	for _, tc := range []struct {
		in, want string
	}{
		{"Some Artist - Some Song [dQw4w9WgXcQ].opus", "dQw4w9WgXcQ"},
		{"Some Artist - Some Song [dQw4w9WgXcQ].m4a", "dQw4w9WgXcQ"},
		{"ripped from a cd.mp3", ""},
	} {
		got := ""
		if m := fileID.FindStringSubmatch(tc.in); m != nil {
			got = m[1]
		}
		if got != tc.want {
			t.Errorf("fileID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// newTestServer points the downloader at a real folder, because enqueue
// refuses to queue anything while the music folder is missing.
func newTestServer(t *testing.T) *server {
	t.Helper()
	d := newDownloader(t.TempDir())
	return &server{addr: "127.0.0.1:7788", token: "sekrit", dir: d.dir,
		enqueue: d.enqueue, available: d.available}
}

func TestHandleAdd(t *testing.T) {
	s := newTestServer(t)

	for _, tc := range []struct {
		name, url string
		code      int
	}{
		{"wrong token", "/add?t=nope&v=dQw4w9WgXcQ", 403},
		{"no token", "/add?v=dQw4w9WgXcQ", 403},
		{"bad id", "/add?t=sekrit&v=whatever", 400},
		{"no id", "/add?t=sekrit", 400},
		{"ok", "/add?t=sekrit&v=dQw4w9WgXcQ", 200},
		{"full url", "/add?t=sekrit&v=https%3A%2F%2Fyoutu.be%2FdQw4w9WgXcQ", 200},
	} {
		rec := httptest.NewRecorder()
		s.handleAdd(rec, httptest.NewRequest("GET", tc.url, nil))
		if rec.Code != tc.code {
			t.Errorf("%s: got %d (%s), want %d", tc.name, rec.Code, strings.TrimSpace(rec.Body.String()), tc.code)
		}
	}
}

func TestHandleAddCORS(t *testing.T) {
	s := newTestServer(t)

	for origin, want := range map[string]string{
		"https://www.youtube.com":   "https://www.youtube.com",
		"https://music.youtube.com": "https://music.youtube.com",
		"https://evil.example":      "",
		"":                          "",
	} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/add?t=sekrit&v=dQw4w9WgXcQ", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		s.handleAdd(rec, r)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != want {
			t.Errorf("origin %q: allow-origin %q, want %q", origin, got, want)
		}
	}
}

func TestEnqueueDoesNotBlock(t *testing.T) {
	d := newDownloader(t.TempDir())
	for i := 0; i < cap(d.queue); i++ {
		if e := d.enqueue("dQw4w9WgXcQ"); e != nil {
			t.Fatalf("enqueue %d: %s", i, e)
		}
	}
	if e := d.enqueue("dQw4w9WgXcQ"); e == nil {
		t.Error("a full queue should report, not block")
	}
}

// The bookmarklet is handed to the page as a JS string and assigned to .href at
// runtime: in a href="" attribute html/template percent-encodes the parens and
// braces, which would ship a broken bookmarklet.
func TestSetupPageKeepsBookmarkletIntact(t *testing.T) {
	s := newTestServer(t)

	rec := httptest.NewRecorder()
	s.handleSetup(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()

	if strings.Contains(body, "ZgotmplZ") {
		t.Error("html/template refused to emit something")
	}
	if strings.Contains(body, "%28") || strings.Contains(body, "%7b") {
		t.Error("bookmarklet got percent-encoded")
	}
	if !strings.Contains(body, s.token) {
		t.Error("token missing from the page")
	}

	assigns := regexp.MustCompile(`\.href = "(.*)";`).FindAllStringSubmatch(body, -1)
	if len(assigns) != 2 {
		t.Fatalf("got %d href assignments, want 2", len(assigns))
	}
	for i, m := range assigns {
		if !strings.Contains(m[1], "javascript:(function()") {
			t.Errorf("href %d is not a bookmarklet: %s", i, m[1])
		}
		if !strings.Contains(m[1], s.addr) {
			t.Errorf("href %d does not point at %s", i, s.addr)
		}
	}
}

func TestLastLine(t *testing.T) {
	for in, want := range map[string]string{
		"a\nb\n/music/x [dQw4w9WgXcQ].opus\n\n": "/music/x [dQw4w9WgXcQ].opus",
		"only\n":                                "only",
		"":                                      "",
		"\n\n\n":                                "",
	} {
		if got := lastLine(in); got != want {
			t.Errorf("lastLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestOverRealSocket drives the endpoints through an actual TCP listener and a
// real HTTP client, the way the bookmarklet does.
func TestOverRealSocket(t *testing.T) {
	d := newDownloader(t.TempDir())
	s := &server{addr: "127.0.0.1:7788", token: "sekrit", dir: d.dir,
		enqueue: d.enqueue, available: d.available}

	ts := httptest.NewServer(s.handler())
	defer ts.Close()

	get := func(path, origin string) (int, string, string) {
		req, e := http.NewRequest("GET", ts.URL+path, nil)
		if e != nil {
			t.Fatal(e)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, strings.TrimSpace(string(b)), res.Header.Get("Access-Control-Allow-Origin")
	}

	if code, _, _ := get("/", ""); code != 200 {
		t.Errorf("setup page: %d", code)
	}
	if code, body, cors := get("/add?t=sekrit&v=dQw4w9WgXcQ", "https://www.youtube.com"); code != 200 || body != "queued" || cors == "" {
		t.Errorf("add: %d %q cors=%q", code, body, cors)
	}
	if code, _, _ := get("/add?t=nope&v=dQw4w9WgXcQ", "https://www.youtube.com"); code != 403 {
		t.Errorf("bad token: %d, want 403", code)
	}
	if code, body, _ := get("/add?close=1&t=sekrit&v=dQw4w9WgXcQ", ""); code != 200 || !strings.Contains(body, "window.close()") {
		t.Errorf("close variant: %d %q", code, body)
	}

	// Two accepted adds must have reached the queue.
	if n := len(d.queue); n != 2 {
		t.Errorf("queued %d ids, want 2", n)
	}
}

// TestBlockWritesArchive checks the bit that stops a removed song coming back.
func TestBlockWritesArchive(t *testing.T) {
	d := newDownloader(t.TempDir())

	if e := d.block(filepath.Join(d.dir, "Some Artist - Some Song [dQw4w9WgXcQ].opus")); e != nil {
		t.Fatal(e)
	}
	// A file we did not download has no id to record, and must not error.
	if e := d.block(filepath.Join(d.dir, "ripped from a cd.mp3")); e != nil {
		t.Fatal(e)
	}
	if e := d.block(filepath.Join(d.dir, "Another - Track [aBcDeFgHiJ_].m4a")); e != nil {
		t.Fatal(e)
	}

	b, e := os.ReadFile(filepath.Join(d.dir, archiveName))
	if e != nil {
		t.Fatal(e)
	}
	got := string(b)
	want := "youtube dQw4w9WgXcQ\nyoutube aBcDeFgHiJ_\n"
	if got != want {
		t.Errorf("archive = %q, want %q", got, want)
	}
}

// TestRefusesWhileFolderMissing covers the case that actually bit: the music
// folder lives on a drive that is not mounted yet.
func TestRefusesWhileFolderMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-mounted")
	d := newDownloader(missing)
	s := &server{addr: "127.0.0.1:7788", token: "sekrit", dir: missing,
		enqueue: d.enqueue, available: d.available}

	if d.available() {
		t.Fatal("a missing folder must not report available")
	}

	// The endpoint still answers; it just refuses to download.
	rec := httptest.NewRecorder()
	s.handleAdd(rec, httptest.NewRequest("GET", "/add?t=sekrit&v=dQw4w9WgXcQ", nil))
	if rec.Code != 503 {
		t.Errorf("add with no folder: %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not available") {
		t.Errorf("unhelpful message: %q", rec.Body.String())
	}
	if _, e := d.fetch("dQw4w9WgXcQ"); e != errNoDir {
		t.Errorf("fetch should refuse, got %v", e)
	}

	// And the setup page says so, rather than leaving you guessing.
	rec = httptest.NewRecorder()
	s.handleSetup(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "does not exist") {
		t.Error("setup page does not mention the missing folder")
	}

	// Once it appears, the same request goes through.
	if e := os.MkdirAll(missing, 0755); e != nil {
		t.Fatal(e)
	}
	rec = httptest.NewRecorder()
	s.handleAdd(rec, httptest.NewRequest("GET", "/add?t=sekrit&v=dQw4w9WgXcQ", nil))
	if rec.Code != 200 {
		t.Errorf("add after folder appeared: %d, want 200", rec.Code)
	}
}

func TestDisplayTitle(t *testing.T) {
	for in, want := range map[string]string{
		// Filename fallback, which is what you get without --embed-metadata.
		"Local Artist - Tone B [aaaaaaaaaaa].opus": "Local Artist - Tone B",
		"Uploader - Song [dQw4w9WgXcQ].m4a":        "Uploader - Song",
		"old collection song.mp4":                  "old collection song",
		// A real tag, which needs no cleanup at all.
		"Some Song":        "Some Song",
		"Some Song [live]": "Some Song [live]",
		// filepath.Ext here is ". Artist"; trimming it would eat the name.
		"Some Song feat. Artist": "Some Song feat. Artist",
		"Track 1. Intro":         "Track 1. Intro",
	} {
		if got := displayTitle(in); got != want {
			t.Errorf("displayTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
