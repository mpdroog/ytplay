package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

// allowedOrigins is the CORS allowlist for /add. A plain GET with no custom
// headers triggers no preflight, so echoing the origin back is all it takes for
// the bookmarklet to be able to read the response.
var allowedOrigins = map[string]bool{
	"https://www.youtube.com":   true,
	"https://music.youtube.com": true,
	"https://m.youtube.com":     true,
	"https://youtube.com":       true,
}

type server struct {
	addr    string
	token   string
	dir     string
	enqueue func(id string) error

	// available reports whether the music folder is mounted, so the setup
	// page can say so instead of leaving you guessing.
	available func() bool
}

// handler wires the two endpoints.
func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/add", s.handleAdd)
	mux.HandleFunc("/", s.handleSetup)
	return mux
}

// ListenAndServe binds the bookmarklet endpoint. The caller passes a loopback
// address: any page in your browser can reach this port, which is why /add
// needs the token below even though it is local.
func (s *server) ListenAndServe() error {
	log.Printf("[http] bookmarklet setup on http://%s/\n", s.addr)
	return http.ListenAndServe(s.addr, s.handler())
}

func (s *server) handleAdd(w http.ResponseWriter, r *http.Request) {
	if o := r.Header.Get("Origin"); allowedOrigins[o] {
		w.Header().Set("Access-Control-Allow-Origin", o)
	}

	q := r.URL.Query()

	// Constant-time so the token cannot be guessed a byte at a time.
	if subtle.ConstantTimeCompare([]byte(q.Get("t")), []byte(s.token)) != 1 {
		http.Error(w, "bad token", http.StatusForbidden)
		return
	}

	id := parseID(q.Get("v"))
	if id == "" {
		http.Error(w, "not a youtube video id", http.StatusBadRequest)
		return
	}

	if e := s.enqueue(id); e != nil {
		http.Error(w, e.Error(), http.StatusServiceUnavailable)
		return
	}

	// The window.open flavour of the bookmarklet lands in a real tab, so give
	// it a page that disposes of itself.
	if q.Get("close") != "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<!doctype html><title>queued</title>queued<script>window.close()</script>")
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, "queued")
}

func (s *server) handleSetup(w http.ResponseWriter, r *http.Request) {
	fetchJS := fmt.Sprintf(bookmarkletFetch, s.addr, s.token)
	openJS := fmt.Sprintf(bookmarkletOpen, s.addr, s.token)

	waiting := s.available != nil && !s.available()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if e := setupPage.Execute(w, map[string]interface{}{
		"Dir":     s.dir,
		"Addr":    s.addr,
		"Waiting": waiting,
		// Handed to the page as JS strings, not as href="": in a URL
		// attribute html/template percent-encodes the parens and braces,
		// and a javascript: href has to survive verbatim.
		"FetchText": fetchJS,
		"OpenText":  openJS,
	}); e != nil {
		log.Printf("[http] setup page: %s\n", e)
	}
}

// The bookmarklets pull the 11-char video id out of the page URL and send only
// that. Sending location.href would drag &list= along and one click on a Mix
// would queue two hundred tracks.
const bookmarkletFetch = `javascript:(function(){var m=location.href.match(/(?:v=|youtu\.be\/|\/shorts\/|\/embed\/|\/live\/)([\w-]{11})/);if(!m){alert('ytplay: no video id in this URL');return}fetch('http://%s/add?t=%s&v='+m[1]).then(function(r){return r.text().then(function(s){if(!r.ok){throw new Error(s)}return s})}).then(function(s){var o=document.title;document.title='✓ '+s;setTimeout(function(){document.title=o},2000)}).catch(function(e){alert('ytplay: '+e.message)})})()`

const bookmarkletOpen = `javascript:(function(){var m=location.href.match(/(?:v=|youtu\.be\/|\/shorts\/|\/embed\/|\/live\/)([\w-]{11})/);if(!m){alert('ytplay: no video id in this URL');return}window.open('http://%s/add?close=1&t=%s&v='+m[1],'_blank')})()`

var setupPage = template.Must(template.New("setup").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>ytplay</title><style>
:root { color-scheme: light dark; --fg: #1a1a1a; --bg: #fdfdfc; --mut: #666; --line: #ddd; }
@media (prefers-color-scheme: dark) { :root { --fg: #e8e8e6; --bg: #1c1c1b; --mut: #999; --line: #333; } }
body { background: var(--bg); color: var(--fg); font: 15px/1.6 system-ui, sans-serif;
       max-width: 42rem; margin: 3rem auto; padding: 0 16px; }
h1 { font-size: 1.3rem; } h2 { font-size: 1rem; margin-top: 2rem; }
p, li { color: var(--mut); } code { color: var(--fg); }
a.bm { display: inline-block; padding: .4rem 1rem; border: 1px solid var(--line);
       border-radius: 6px; font-weight: 600; text-decoration: none; color: var(--fg); cursor: grab; }
p.warn { color: #b3261e; } @media (prefers-color-scheme: dark) { p.warn { color: #f2b8b5; } }
textarea { width: 100%; height: 5rem; font: 11px/1.4 ui-monospace, monospace;
           background: transparent; color: var(--mut); border: 1px solid var(--line); border-radius: 6px; padding: .5rem; }
</style></head><body>
<h1>ytplay</h1>
<p>Collection: <code>{{.Dir}}</code> &middot; listening on <code>{{.Addr}}</code></p>
{{if .Waiting}}<p class="warn"><strong>{{.Dir}} does not exist.</strong> Downloads are
refused until it shows up. Point <code>-p</code> at your collection, or create the folder.</p>{{end}}

<h2>1. Drag this to your Bookmarks Toolbar</h2>
<p><a class="bm" id="bm-fetch" href="#">+ ytplay</a></p>
<p>Then click it while a video plays. The tab title flashes <code>&check; queued</code>,
and a desktop notification tells you when the file has landed.
Your token is baked into the link &mdash; treat it like a password.</p>

<h2>2. If nothing happens</h2>
<p>Some browsers block a request from an <code>https:</code> page to a local address.
Use this one instead: it opens a tab that closes itself, which cannot be blocked.</p>
<p><a class="bm" id="bm-open" href="#">+ ytplay (tab)</a></p>

<h2>Can't drag?</h2>
<p>New bookmark, paste as the URL:</p>
<textarea readonly onclick="this.select()">{{.FetchText}}</textarea>
<textarea readonly onclick="this.select()">{{.OpenText}}</textarea>

<h2>Keyboard only</h2>
<p>Give the bookmark a keyword (say <code>add</code>) in its properties, then
<code>Ctrl+L</code>, <code>add</code>, <code>Enter</code>. Some builds refuse to run a
bookmarklet from the address bar; the toolbar click always works.</p>
<script>
document.getElementById("bm-fetch").href = "{{.FetchText}}";
document.getElementById("bm-open").href = "{{.OpenText}}";
</script>
</body></html>
`))

// loadToken reads the shared secret from the user config dir, creating it on
// first run. It has to survive restarts or every bookmarklet would go stale.
func loadToken() (string, error) {
	cfg, e := os.UserConfigDir()
	if e != nil {
		return "", e
	}
	dir := filepath.Join(cfg, "ytplay")
	if e := os.MkdirAll(dir, 0700); e != nil {
		return "", e
	}

	name := filepath.Join(dir, "token")
	if b, e := os.ReadFile(name); e == nil && len(b) > 0 {
		return string(trimSpaceBytes(b)), nil
	}

	buf := make([]byte, 16)
	if _, e := rand.Read(buf); e != nil {
		return "", e
	}
	token := hex.EncodeToString(buf)

	if e := os.WriteFile(name, []byte(token+"\n"), 0600); e != nil {
		return "", e
	}
	log.Printf("[http] new token written to %s\n", name)

	return token, nil
}

func trimSpaceBytes(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}
