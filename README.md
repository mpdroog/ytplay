YTPlay
====================
Play random songs from the terminal with VLC, and add new ones from Firefox with
one click.

Successor to [vlcplay](https://github.com/mpdroog/vlcplay): same low-CPU list
player, plus a loopback HTTP endpoint that a bookmarklet talks to, so adding the
song you are listening to on youtube never means opening a terminal.

Using https://pkg.go.dev/github.com/adrg/libvlc-go/v3

Install
--------------------
	apk add vlc vlc-dev ffmpeg yt-dlp     # alpine
	./build.sh

`vlc` and `vlc-dev` are both needed: `vlc-dev` to compile against, and the
plugin tree from `vlc` to decode anything at runtime. With only the libraries
installed, startup fails with `could not initialize module`.

Build through `build.sh`, or export `CGO_ENABLED=1` yourself. libvlc-go is a cgo
package, and with cgo off the go tool drops every file in it and reports
`undefined: vlc.Init` instead of anything mentioning cgo.

libvlc-go v3.1.6 or newer is required. v3.1.5 declares an event callback without
`const`, which gcc 14 and newer reject outright:
`passing argument 3 of 'libvlc_event_attach' from incompatible pointer type`.

Distro-packaged yt-dlp goes stale quickly, and stale yt-dlp is how youtube
breaks you. Either run `yt-dlp -U` on the standalone binary or install it with
`pipx` so it updates independently of the distro.

Run
--------------------
	./ytplay -p ~/Music

Then open http://127.0.0.1:7788/ once and drag the `+ ytplay` link to your
Bookmarks Toolbar. From then on: click it while a video plays. The tab title
flashes a check mark, and a desktop notification tells you when the file landed.
The song joins the rotation immediately, no restart.

An empty collection is fine: ytplay stays up, says it is waiting, and starts
playing as soon as your first download lands. So a fresh install is: start it,
open the page, click the bookmarklet on a song.

The page carries a second bookmarklet for the case where your browser blocks a
request from an https page to a local address. It opens a tab that closes
itself, which cannot be blocked.

Flags
--------------------
	-p    music folder (default ~/Music)
	-l    bookmarklet listen address (default 127.0.0.1:7788, empty to disable)
	-dl   download this URL or video id and exit, no player
	-r    retry every 5sec until the music folder shows up
	-v    verbose

Keys
--------------------
	n              next song
	p              previous song
	t              pause or resume
	r              remove the playing song, and never download it again
	d <url>        download a song

Why not mp3
--------------------
Youtube serves opus or aac, and both play fine here. Converting to mp3 would
cost a second lossy generation and a lot of CPU for nothing, so the audio stream
is remuxed instead of re-encoded: same bytes, video track dropped. That is a
~10x smaller file than the mp4 and less to read while playing.

Only reach for mp3 if some other device demands it.

The archive
--------------------
`.ytplay-archive` in the music folder is yt-dlp's `--download-archive`: one line
per song ever fetched. Removing a song with `r` appends its id there too, so
something you threw away does not come back on the next fetch. Delete a line to
allow a song again.

Filenames keep the video id (`Uploader - Title [videoid].opus`), which is how a
file on disk maps back to a line in the archive.

The token
--------------------
Any page in your browser can reach a port on localhost, so `/add` requires a
token. It is generated on first run, kept in `~/.config/ytplay/token`, and baked
into the bookmarklet link; treat that link like a password. The server binds
loopback only and takes nothing but an 11-character video id.
