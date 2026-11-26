package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/gen2brain/beeep"
)

// archiveName is yt-dlp's --download-archive: one "youtube <id>" line per
// fetched video. Removing a song appends its id here too, so a song you threw
// away never comes back on the next fetch.
const archiveName = ".ytplay-archive"

// outTemplate keeps the video id in the filename so we can map a file on disk
// back to the id the archive speaks in.
const outTemplate = "%(uploader)s - %(title)s [%(id)s].%(ext)s"

var (
	bareID  = regexp.MustCompile(`^[\w-]{11}$`)
	urlID   = regexp.MustCompile(`(?:v=|youtu\.be/|/shorts/|/embed/|/live/)([\w-]{11})`)
	fileID  = regexp.MustCompile(`\[([\w-]{11})\]\.[^.]+$`)
	errFull = errors.New("download queue is full")

	// The music folder may live on a drive that mounts later. Downloading
	// before it shows up would quietly fill the mountpoint on the wrong
	// filesystem, so we refuse instead.
	errNoDir = errors.New("music folder is not available yet")
)

// parseID accepts a bare video id or any youtube URL shape and returns the id,
// or "" when there is none. Only the id travels further: a watch URL copied
// from a Mix carries &list=, and we are never interested in the playlist.
func parseID(s string) string {
	s = strings.TrimSpace(s)
	if bareID.MatchString(s) {
		return s
	}
	if m := urlID.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

type downloader struct {
	dir   string
	queue chan string

	// added is called with the path of each newly fetched file, from the
	// worker goroutine. It is set once the player is up, which is after the
	// worker starts, hence the mutex.
	mu    sync.Mutex
	added func(path string)
}

// setAdded hands the downloader somewhere to put finished songs.
func (d *downloader) setAdded(fn func(path string)) {
	d.mu.Lock()
	d.added = fn
	d.mu.Unlock()
}

func (d *downloader) notifyAdded(path string) {
	d.mu.Lock()
	fn := d.added
	d.mu.Unlock()

	if fn != nil {
		fn(path)
	}
}

// available reports whether the music folder is there to write into.
func (d *downloader) available() bool {
	fi, e := os.Stat(d.dir)
	return e == nil && fi.IsDir()
}

func newDownloader(dir string) *downloader {
	return &downloader{dir: dir, queue: make(chan string, 32)}
}

// enqueue hands an id to the worker without blocking the caller (the HTTP
// handler, or the stdin loop).
func (d *downloader) enqueue(id string) error {
	if !d.available() {
		return errNoDir
	}

	select {
	case d.queue <- id:
		return nil
	default:
		return errFull
	}
}

// run drains the queue one job at a time. It never exits on a failed download:
// a dead link must not stop the music.
func (d *downloader) run() {
	for id := range d.queue {
		if verbose {
			log.Printf("[dl] fetching %s\n", id)
		}

		path, e := d.fetch(id)
		if e != nil {
			notify("Download failed", id+": "+e.Error())
			continue
		}
		if path == "" {
			notify("Already in collection", id)
			continue
		}
		d.notifyAdded(path)
		notify("Added", filepath.Base(path))
	}
}

// fetch runs yt-dlp and returns the path of the downloaded file, or "" when the
// video was already in the archive.
//
// The audio stream is remuxed, never re-encoded: youtube serves opus or aac and
// both play fine, so converting to mp3 would only cost a second lossy
// generation and a lot of CPU.
func (d *downloader) fetch(id string) (string, error) {
	// Re-check: the drive can go away between queueing and running.
	if !d.available() {
		return "", errNoDir
	}

	archive := filepath.Join(d.dir, archiveName)
	cmd := exec.Command("yt-dlp",
		"--no-playlist",
		"-f", "bestaudio",
		"-x", "--audio-format", "best",
		"--embed-metadata", "--embed-thumbnail",
		"--download-archive", archive,
		// --print implies --simulate, so switch it back off. after_move
		// gives the final path, i.e. after the remux renamed the file.
		"--no-simulate", "--print", "after_move:filepath",
		"-o", outTemplate,
		"-P", d.dir,
		"https://www.youtube.com/watch?v="+id,
	)

	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	if e := cmd.Run(); e != nil {
		if msg := lastLine(stderr.String()); msg != "" {
			return "", fmt.Errorf("yt-dlp: %s", msg)
		}
		return "", fmt.Errorf("yt-dlp: %s", e)
	}

	// Nothing printed means yt-dlp skipped it on the archive.
	return lastLine(out.String()), nil
}

// block records a song in the archive so it is never fetched again. Called when
// you remove the currently playing song.
func (d *downloader) block(path string) error {
	m := fileID.FindStringSubmatch(filepath.Base(path))
	if m == nil {
		// Not one of ours (no [id] in the name), nothing to record.
		return nil
	}

	f, e := os.OpenFile(filepath.Join(d.dir, archiveName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if e != nil {
		return e
	}
	defer f.Close()

	_, e = fmt.Fprintf(f, "youtube %s\n", m[1])
	return e
}

// lastLine returns the last non-empty line of s, trimmed.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// notify reports to the terminal and to the desktop, because the whole point is
// that you are in the browser and not looking at a terminal.
func notify(title, msg string) {
	log.Printf("%s: %s\n", title, msg)
	if e := beeep.Notify("ytplay: "+title, msg, ""); e != nil && verbose {
		log.Printf("[notify] %s\n", e)
	}
}
