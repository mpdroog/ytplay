package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	vlc "github.com/adrg/libvlc-go/v3"
)

// playable is every container we may end up with. The originals from youtube are
// mp4/webm; extracted audio lands as m4a (aac) or opus.
var playable = map[string]bool{
	".mp4": true, ".webm": true, ".m4a": true, ".opus": true,
	".mp3": true, ".ogg": true, ".oga": true, ".flac": true, ".aac": true,
}

var verbose bool

// nowPlaying is written by the libVLC event callback and read by the stdin
// loop, so it needs the mutex.
type nowPlaying struct {
	sync.Mutex
	path string
}

func (n *nowPlaying) set(path string) {
	n.Lock()
	n.path = path
	n.Unlock()
}

func (n *nowPlaying) get() string {
	n.Lock()
	defer n.Unlock()
	return n.path
}

func main() {
	home, _ := os.UserHomeDir()

	wait := false
	path := ""
	listen := ""
	fetch := ""
	flag.StringVar(&path, "p", filepath.Join(home, "Music"), "Music folder")
	flag.BoolVar(&verbose, "v", false, "Increase verbosity")
	flag.BoolVar(&wait, "r", true, "Retry every 5sec if path is available")
	flag.StringVar(&listen, "l", "127.0.0.1:7788", "Listen address for the browser bookmarklet, empty to disable")
	flag.StringVar(&fetch, "dl", "", "Download this youtube URL (or video id) and exit")
	flag.Parse()

	dl := newDownloader(path)

	// Fetch-only mode: no libVLC, no player, just grab the song. Handy from a
	// script or another terminal while the player runs elsewhere.
	if fetch != "" {
		id := parseID(fetch)
		if id == "" {
			log.Fatalf("not a youtube URL or video id: %s", fetch)
		}
		file, e := dl.fetch(id)
		if e != nil {
			log.Fatal(e)
		}
		if file == "" {
			fmt.Println("already in the collection")
			return
		}
		fmt.Println(file)
		return
	}

	// The bookmarklet endpoint comes up first, on purpose. It must not be
	// hostage to libVLC starting or to the music folder showing up, because
	// those are exactly the moments you want to see what is wrong.
	if listen != "" {
		token, e := loadToken()
		if e != nil {
			log.Fatal(e)
		}
		srv := &server{
			addr:      listen,
			token:     token,
			dir:       path,
			enqueue:   dl.enqueue,
			available: dl.available,
		}
		go func() {
			if e := srv.ListenAndServe(); e != nil {
				log.Printf("[http] %s\n", e)
			}
		}()
	}
	go dl.run()

	// Initialize libVLC. Additional command line arguments can be passed in
	// to libVLC by specifying them in the Init function.
	if err := vlc.Init("--no-video", "--quiet"); err != nil {
		// The usual cause is libvlc without its plugin tree: the libraries
		// alone are not enough to decode anything.
		log.Fatalf("%s (is the vlc package installed, not just vlc-dev?)", err)
	}
	defer vlc.Release()

	// Create a new list player.
	player, err := vlc.NewListPlayer()
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		player.Stop()
		player.Release()
	}()

	// Create a new media list.
	list, err := vlc.NewMediaList()
	if err != nil {
		log.Fatal(err)
	}
	defer list.Release()

	if wait {
		for {
			_, e := os.Stat(path)
			if errors.Is(e, os.ErrNotExist) {
				log.Printf("waiting for %s to show up (downloads are refused until it does)\n", path)
				time.Sleep(time.Second * 5)
				continue
			}
			if e != nil {
				log.Fatal(e)
			}

			// file exists
			break
		}
	}

	songs, e := scan(path)
	if e != nil {
		log.Fatal(e)
	}
	if verbose {
		fmt.Printf("song.count=%d\n", len(songs))
	}

	// Shuffle up front; the list player only knows Loop.
	rand.Shuffle(len(songs), func(i, j int) {
		songs[i], songs[j] = songs[j], songs[i]
	})

	for _, song := range songs {
		if err := list.AddMediaFromPath(song); err != nil {
			log.Fatal(err)
		}
	}

	// Set player media list.
	if err = player.SetMediaList(list); err != nil {
		log.Fatal(err)
	}

	// Retrieve player event manager.
	manager, err := player.EventManager()
	if err != nil {
		log.Fatal(err)
	}

	// remember currently played song (for deleting with 'r')
	current := &nowPlaying{}

	// inList tracks how many songs the media list holds. The event callback
	// runs on a libVLC thread, so it reads this instead of calling
	// list.Count(), which would take the media list lock underneath libVLC.
	var inList atomic.Int64
	inList.Store(int64(len(songs)))

	// Register the media end reached event with the event manager.
	quit := make(chan struct{})
	var quitOnce sync.Once
	stop := func() { quitOnce.Do(func() { close(quit) }) }

	// Playback starts on the first song, which may arrive from a download
	// rather than from the initial scan.
	var playOnce sync.Once
	startPlayback := func() {
		playOnce.Do(func() {
			if e := player.Play(); e != nil {
				log.Println(e)
			}
		})
	}

	eventCallback := func(event vlc.Event, userData interface{}) {
		switch event {
		case vlc.MediaListPlayerPlayed:
			// The list ran out. With Loop that only happens when it is
			// empty, so wait for the first download instead of exiting.
			if inList.Load() == 0 {
				break
			}
			stop()
		case vlc.MediaListPlayerNextItemSet:
			// Retrieve underlying player.
			p, err := player.Player()
			if err != nil {
				log.Println(err)
				break
			}

			// Retrieve currently playing media.
			media, err := p.Media()
			if err != nil {
				log.Println(err)
				break
			}

			song, e := media.Meta(vlc.MediaTitle)
			if e != nil {
				log.Println(e)
				break
			}

			// Location() hands back a filesystem path, not an MRL.
			location, err := media.Location()
			if err != nil {
				log.Println(err)
				break
			}
			current.set(location)

			title := displayTitle(song)

			fmt.Printf("\033]1;" + title + " \007")
			log.Println("Now playing:", title)

		default:
			fmt.Printf("Event(%d) data=%+v\n", event, userData)
		}
	}

	var eventIDs []vlc.EventID
	for _, event := range []vlc.Event{vlc.MediaListPlayerPlayed, vlc.MediaListPlayerNextItemSet} {
		eventID, err := manager.Attach(event, eventCallback, nil)
		if err != nil {
			log.Fatal(err)
		}
		eventIDs = append(eventIDs, eventID)
	}
	defer manager.Detach(eventIDs...)

	if err := player.SetPlaybackMode(vlc.Loop); err != nil {
		log.Fatal(err)
	}

	// A freshly downloaded song joins the rotation without a restart.
	// AddMedia takes the media list lock itself, so this is safe from the
	// download goroutine while playback runs.
	dl.setAdded(func(file string) {
		if e := list.AddMediaFromPath(file); e != nil {
			log.Printf("[dl] could not queue %s: %s\n", file, e)
			return
		}
		inList.Add(1)

		// First song of an empty collection: get it playing.
		startPlayback()
	})

	go readCommands(player, list, dl, current)

	if len(songs) == 0 {
		// Nothing to play yet. Staying up is the point: this is how a fresh
		// collection gets its first song.
		log.Printf("no songs in %s yet, waiting for a download\n", path)
		if listen != "" {
			log.Printf("add one from the browser: http://%s/\n", listen)
		}
	} else {
		startPlayback()
	}

	<-quit
}

// titleID matches the " [videoid]" that outTemplate puts in filenames.
var titleID = regexp.MustCompile(` \[[\w-]{11}\]$`)

// displayTitle tidies what VLC hands us. With --embed-metadata that is the real
// song title and nothing needs doing; without it VLC falls back to the
// filename, so drop the extension and the video id.
//
// Only a known extension is stripped: filepath.Ext of a title like
// "Some Song feat. Artist" is ". Artist", and trimming that would eat the name.
func displayTitle(meta string) string {
	if ext := filepath.Ext(meta); playable[strings.ToLower(ext)] {
		meta = strings.TrimSuffix(meta, ext)
	}
	return titleID.ReplaceAllString(meta, "")
}

// scan collects every playable file below root.
func scan(root string) ([]string, error) {
	songs := []string{}

	e := filepath.WalkDir(root, func(s string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		name := d.Name()
		if strings.HasPrefix(name, "._") {
			// ignore ._ files (meta?)
			return nil
		}
		if !playable[strings.ToLower(filepath.Ext(name))] {
			// Only add playable music/video files
			return nil
		}

		if verbose {
			fmt.Printf("[sync] Read %s\n", s)
		}
		songs = append(songs, s)
		return nil
	})

	return songs, e
}

func readCommands(player *vlc.ListPlayer, list *vlc.MediaList, dl *downloader, current *nowPlaying) {
	cmds := map[string]string{
		"n": "Next song", "p": "Previous song", "t": "Pause or resume",
		"r": "Remove song (and never download it again)",
		"d": "d <youtube url> to download",
	}

	reader := bufio.NewReader(os.Stdin)
	for {
		line, e := reader.ReadString('\n')
		if e != nil {
			// No terminal attached (or it went away). Keep playing.
			if verbose {
				log.Printf("[stdin] %s, stopping command reader\n", e)
			}
			return
		}

		cmd := strings.TrimSpace(line)
		arg := ""
		if i := strings.IndexByte(cmd, ' '); i != -1 {
			cmd, arg = cmd[:i], strings.TrimSpace(cmd[i+1:])
		}

		switch cmd {
		case "n":
			if e := player.PlayNext(); e != nil {
				log.Println(e)
			}
		case "p":
			if e := player.PlayPrevious(); e != nil {
				log.Println(e)
			}
		case "t":
			if e := player.TogglePause(); e != nil {
				log.Println(e)
				break
			}
			fmt.Printf("Toggle pause\n")
		case "r":
			remove(player, list, dl, current.get())
		case "d":
			id := parseID(arg)
			if id == "" {
				fmt.Printf("Not a youtube URL or video id: %s\n", arg)
				break
			}
			if e := dl.enqueue(id); e != nil {
				fmt.Printf("%s\n", e)
			}
		default:
			fmt.Printf("Unsupported cmd, possible=\n")
			fmt.Printf("%+v\n", cmds)
		}
	}
}

// remove skips the current song, drops it from the playlist, records its id so
// it is never fetched again, and deletes the file.
func remove(player *vlc.ListPlayer, list *vlc.MediaList, dl *downloader, file string) {
	if file == "" {
		fmt.Printf("Nothing playing\n")
		return
	}

	if e := player.PlayNext(); e != nil {
		log.Println(e)
		return
	}
	if e := dropFromList(list, file); e != nil {
		log.Println(e)
	}
	if e := dl.block(file); e != nil {
		log.Println(e)
	}
	if e := os.Remove(file); e != nil {
		log.Println(e)
		return
	}

	fmt.Printf("Removed %s\n", file)
}

// dropFromList removes file from the media list so Loop will not come back to a
// path that is gone. Every MediaList accessor takes the list lock itself, so we
// must not hold it here.
func dropFromList(list *vlc.MediaList, file string) error {
	count, e := list.Count()
	if e != nil {
		return e
	}

	for i := 0; i < count; i++ {
		media, e := list.MediaAtIndex(uint(i))
		if e != nil {
			return e
		}
		loc, e := media.Location()
		if e != nil {
			return e
		}
		if loc == file {
			return list.RemoveMediaAtIndex(uint(i))
		}
	}

	return nil
}
