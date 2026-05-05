package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// pickFile shells out to the platform's native file dialog.
func pickFile() (string, error) {
	switch runtime.GOOS {
	case "linux":
		if p, err := exec.LookPath("zenity"); err == nil {
			out, err := exec.Command(p, "--file-selection",
				"--title=Open .miniscram or .cue",
				"--file-filter=miniscram + cue | *.miniscram *.cue",
				"--file-filter=all files | *").Output()
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(out)), nil
		}
		if p, err := exec.LookPath("kdialog"); err == nil {
			out, err := exec.Command(p, "--getopenfilename", "",
				"*.miniscram *.cue|miniscram + cue\n*|all files").Output()
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(out)), nil
		}
		return "", errors.New("install zenity or kdialog for the native picker")
	case "darwin":
		out, err := exec.Command("osascript", "-e",
			`POSIX path of (choose file with prompt "Open .miniscram or .cue" of type {"miniscram","cue"})`).Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	case "windows":
		ps := `Add-Type -AssemblyName System.Windows.Forms;` +
			`$f = New-Object System.Windows.Forms.OpenFileDialog;` +
			`$f.Filter = "miniscram + cue|*.miniscram;*.cue|All|*";` +
			`if ($f.ShowDialog() -eq 'OK') { $f.FileName }`
		out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
	return "", fmt.Errorf("no file picker for %s", runtime.GOOS)
}

// pickSave shells out to the platform's native save dialog.
func pickSave(defaultName, defaultDir string) (string, error) {
	switch runtime.GOOS {
	case "linux":
		if p, err := exec.LookPath("zenity"); err == nil {
			out, err := exec.Command(p, "--file-selection",
				"--save", "--confirm-overwrite",
				"--title=Save .scram as…",
				"--filename="+filepath.Join(defaultDir, defaultName)).Output()
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(out)), nil
		}
		if p, err := exec.LookPath("kdialog"); err == nil {
			out, err := exec.Command(p, "--getsavefilename",
				filepath.Join(defaultDir, defaultName), "*.scram|scram\n*|all files").Output()
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(out)), nil
		}
		return "", errors.New("install zenity or kdialog for the native save picker")
	case "darwin":
		// AppleScript double-quoted string escapes: \ and " must be escaped.
		asEscape := func(s string) string {
			s = strings.ReplaceAll(s, `\`, `\\`)
			s = strings.ReplaceAll(s, `"`, `\"`)
			return s
		}
		script := fmt.Sprintf(
			`POSIX path of (choose file name with prompt "Save .scram as…" default name "%s" default location POSIX file "%s")`,
			asEscape(defaultName), asEscape(defaultDir))
		out, err := exec.Command("osascript", "-e", script).Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	case "windows":
		// PowerShell single-quoted strings don't interpret $ or backtick;
		// only ' needs to be escaped (by doubling). Preserves legal NTFS
		// chars like $ and backtick in user paths instead of stripping.
		psQuote := func(s string) string {
			return "'" + strings.ReplaceAll(s, "'", "''") + "'"
		}
		ps := fmt.Sprintf(`Add-Type -AssemblyName System.Windows.Forms;`+
			`$f = New-Object System.Windows.Forms.SaveFileDialog;`+
			`$f.FileName = %s;`+
			`$f.InitialDirectory = %s;`+
			`$f.Filter = 'scram|*.scram|All|*';`+
			`$f.OverwritePrompt = $true;`+
			`if ($f.ShowDialog() -eq 'OK') { $f.FileName }`,
			psQuote(defaultName), psQuote(defaultDir))
		out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
	return "", fmt.Errorf("no save dialog for %s", runtime.GOOS)
}

const guiVersion = "0.1"
const userAgent = "miniscram-gui/" + guiVersion + " (+https://github.com/hughobrien/miniscram) Go-http-client/1.1"

// ---------------- inspect json ----------------

type inspectJSON struct {
	ToolVersion      string `json:"tool_version"`
	CreatedUnix      int64  `json:"created_unix"`
	WriteOffsetBytes int    `json:"write_offset_bytes"`
	LeadinLBA        int    `json:"leadin_lba"`
	Scram            struct {
		Size   int64             `json:"size"`
		Hashes map[string]string `json:"hashes"`
	} `json:"scram"`
	Tracks []struct {
		Number   int               `json:"number"`
		Mode     string            `json:"mode"`
		FirstLBA int               `json:"first_lba"`
		Filename string            `json:"filename"`
		Size     int64             `json:"size"`
		Hashes   map[string]string `json:"hashes"`
	} `json:"tracks"`
	DeltaRecords []json.RawMessage `json:"delta_records"`
}

type cueTrack struct {
	num      int
	mode     string
	filename string
	size     int64
	exists   bool
}

// ---------------- redump lookup ----------------

type redumpEntry struct {
	State       string `json:"state"` // "found" | "miss" | "err" | "pending"
	URL         string `json:"url,omitempty"`
	Title       string `json:"title,omitempty"`
	CheckedUnix int64  `json:"checked_unix"`
}

var titleRe = regexp.MustCompile(`<title>redump\.org\s*&bull;\s*([^<]+?)\s*</title>`)

func redumpFetch(hash string) *redumpEntry {
	now := time.Now().Unix()
	req, err := http.NewRequest("GET", "http://redump.org/discs/quicksearch/"+url.PathEscape(hash)+"/", nil)
	if err != nil {
		return &redumpEntry{State: "err", CheckedUnix: now}
	}
	req.Header.Set("User-Agent", userAgent)
	cli := &http.Client{Timeout: 15 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return &redumpEntry{State: "err", CheckedUnix: now}
	}
	defer resp.Body.Close()
	final := resp.Request.URL.String()
	if !strings.Contains(final, "/disc/") {
		return &redumpEntry{State: "miss", CheckedUnix: now}
	}
	body, _ := io.ReadAll(resp.Body)
	title := ""
	if m := titleRe.FindStringSubmatch(string(body)); len(m) > 1 {
		t := strings.TrimSpace(m[1])
		t = strings.SplitN(t, "&bull;", 2)[0]
		title = strings.TrimSpace(t)
	}
	return &redumpEntry{State: "found", URL: final, Title: title, CheckedUnix: now}
}

// ---------------- model ----------------

type modeHover struct{ hovered bool }

func (m *model) getDeleteBtn(id int64) *widget.Clickable {
	if m.deleteBtns == nil {
		m.deleteBtns = map[int64]*widget.Clickable{}
	}
	if c, ok := m.deleteBtns[id]; ok {
		return c
	}
	c := new(widget.Clickable)
	m.deleteBtns[id] = c
	return c
}

type model struct {
	db *sql.DB

	view string // "file" | "stats"

	path     string
	basename string
	dir      string
	kind     string
	err      string

	meta            *inspectJSON
	miniscramOnDisk int64

	cueTracks []cueTrack
	cueText   string

	miniscramVersion string

	redump     map[string]*redumpEntry
	redumpMu   sync.Mutex
	invalidate func()

	stats       statsAgg
	recent      []eventRec
	statsLoaded bool

	// per-row hover state for mode chips (track row 0..N)
	modeHover map[int]*modeHover

	// per-row delete buttons for stats events
	deleteBtns map[int64]*widget.Clickable

	runner *actionRunner
	toast  *toastState
	queue  *queueModel
}

func (m *model) load(p string) {
	m.path = p
	m.basename = filepath.Base(p)
	m.dir = filepath.Dir(p)
	m.kind = ""
	m.err = ""
	m.meta = nil
	m.miniscramOnDisk = 0
	m.cueTracks = nil
	m.cueText = ""
	if m.redump == nil {
		m.redump = map[string]*redumpEntry{}
	}

	switch strings.ToLower(filepath.Ext(p)) {
	case ".miniscram":
		m.kind = "miniscram"
		raw, err := exec.Command("miniscram", "inspect", p, "--json").Output()
		if err != nil {
			m.err = err.Error()
			return
		}
		var meta inspectJSON
		if err := json.Unmarshal(raw, &meta); err != nil {
			m.err = err.Error()
			return
		}
		m.meta = &meta
		if st, err := os.Stat(p); err == nil {
			m.miniscramOnDisk = st.Size()
		}
		var hashes []string
		for _, t := range m.meta.Tracks {
			if h := t.Hashes["sha1"]; h != "" {
				hashes = append(hashes, h)
			}
		}
		go m.lookup(hashes)
	case ".cue":
		m.kind = "cue"
		b, err := os.ReadFile(p)
		if err != nil {
			m.err = err.Error()
			return
		}
		m.cueText = string(b)
		m.cueTracks = parseCueLines(m.cueText, m.dir)
	default:
		m.err = "drop a .miniscram or a .cue"
	}
}

func (m *model) lookup(hashes []string) {
	for _, h := range hashes {
		// disk cache first
		if e, ok := redumpGet(m.db, h); ok {
			m.redumpMu.Lock()
			m.redump[h] = e
			m.redumpMu.Unlock()
			if m.invalidate != nil {
				m.invalidate()
			}
			continue
		}
		m.redumpMu.Lock()
		if existing, ok := m.redump[h]; ok && existing != nil && existing.State != "" && existing.State != "pending" {
			m.redumpMu.Unlock()
			continue
		}
		m.redump[h] = &redumpEntry{State: "pending"}
		m.redumpMu.Unlock()
		if m.invalidate != nil {
			m.invalidate()
		}
		e := redumpFetch(h)
		redumpPut(m.db, h, e)
		m.redumpMu.Lock()
		m.redump[h] = e
		m.redumpMu.Unlock()
		if m.invalidate != nil {
			m.invalidate()
		}
	}
}

func (m *model) refreshStats() {
	m.stats = eventsAggregate(m.db)
	m.recent = eventsRecent(m.db, 25)
	m.statsLoaded = true
}

// handleActionResult translates a runner actionResult into an event row,
// persists it, and refreshes the stats view. Runs on the UI goroutine
// (called from the FrameEvent drain).
// buildEventRec turns an actionResult into a populated eventRec.
// Pure (no DB writes, no toast); shared by handleActionResult and the queue worker.
func buildEventRec(m *model, action, input, output string, res actionResult) eventRec {
	ev := eventRec{
		TS:         time.Now(),
		Action:     action,
		InputPath:  input,
		OutputPath: output,
		DurationMs: res.DurationMs,
		Status:     res.Status,
		Error:      res.Error,
	}
	fillTitle := func(meta *inspectJSON) {
		if meta == nil || len(meta.Tracks) == 0 {
			return
		}
		if e, ok := redumpGet(m.db, meta.Tracks[0].Hashes["sha1"]); ok && e.State == "found" {
			ev.Title = e.Title
		}
	}
	if res.Status != "success" {
		return ev
	}
	switch action {
	case "pack":
		if output != "" {
			if st, err := os.Stat(output); err == nil {
				ev.MiniscramSize = st.Size()
			}
			if raw, err := exec.Command("miniscram", "inspect", output, "--json").Output(); err == nil {
				var meta inspectJSON
				if json.Unmarshal(raw, &meta) == nil {
					ev.ScramSize = meta.Scram.Size
					ev.OverrideRecords = len(meta.DeltaRecords)
					ev.WriteOffset = meta.WriteOffsetBytes
					fillTitle(&meta)
				}
			}
		}
	case "unpack":
		if output != "" {
			if st, err := os.Stat(output); err == nil {
				ev.ScramSize = st.Size()
			}
		}
		if m.meta != nil {
			ev.MiniscramSize = m.miniscramOnDisk
			ev.OverrideRecords = len(m.meta.DeltaRecords)
			ev.WriteOffset = m.meta.WriteOffsetBytes
			fillTitle(m.meta)
		}
	case "verify":
		if m.meta != nil {
			ev.ScramSize = m.meta.Scram.Size
			ev.MiniscramSize = m.miniscramOnDisk
			ev.OverrideRecords = len(m.meta.DeltaRecords)
			ev.WriteOffset = m.meta.WriteOffsetBytes
			fillTitle(m.meta)
		}
	}
	return ev
}

func (m *model) handleActionResult(res actionResult) {
	ev := buildEventRec(m, res.Action, res.Input, res.Output, res)
	eventInsert(m.db, ev)
	m.refreshStats()

	// Populate or clear the toast based on outcome.
	if res.Status == "success" {
		var outputSize int64
		switch res.Action {
		case "pack":
			outputSize = ev.MiniscramSize
		case "unpack":
			outputSize = ev.ScramSize
		}
		m.toast = &toastState{
			Action:     res.Action,
			Output:     res.Output,
			OutputSize: outputSize,
			DurationMs: res.DurationMs,
			ExpiresAt:  time.Now().Add(6 * time.Second),
		}
	} else {
		m.toast = nil
	}
}

func parseCueLines(s, baseDir string) []cueTrack {
	var out []cueTrack
	var pending string
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "FILE "):
			f := strings.TrimPrefix(t, "FILE ")
			f = strings.TrimSuffix(f, " BINARY")
			f = strings.TrimSuffix(f, " WAVE")
			f = strings.TrimSuffix(f, " MP3")
			f = strings.Trim(f, "\"")
			pending = f
		case strings.HasPrefix(t, "TRACK "):
			parts := strings.Fields(t)
			if len(parts) >= 3 {
				var n int
				_, _ = fmt.Sscanf(parts[1], "%d", &n)
				ct := cueTrack{num: n, mode: parts[2], filename: pending}
				if pending != "" {
					if st, err := os.Stat(filepath.Join(baseDir, pending)); err == nil {
						ct.size = st.Size()
						ct.exists = true
					}
				}
				out = append(out, ct)
			}
		}
	}
	return out
}

// ---------------- helpers ----------------

func humanBytes(n int64) string {
	const k = 1024
	if n < k {
		return fmt.Sprintf("%d B", n)
	}
	units := "KMGTPE"
	div, exp := int64(k), 0
	for n/div >= k && exp < len(units)-1 {
		div *= k
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), units[exp])
}

func ratioStr(scram, mini int64) string {
	if mini == 0 || scram == 0 {
		return "—"
	}
	r := float64(scram) / float64(mini)
	switch {
	case r >= 1_000_000:
		return fmt.Sprintf("%.1fM×", r/1_000_000)
	case r >= 1_000:
		return fmt.Sprintf("%.0fK×", r/1_000)
	default:
		return fmt.Sprintf("%.0f×", math.Round(r))
	}
}

func ratioFloat(scram, mini int64) string {
	if mini == 0 || scram == 0 {
		return "—"
	}
	r := float64(scram) / float64(mini)
	switch {
	case r >= 1_000_000:
		return fmt.Sprintf("%.2fM×", r/1_000_000)
	case r >= 1_000:
		return fmt.Sprintf("%.1fK×", r/1_000)
	default:
		return fmt.Sprintf("%.0f×", math.Round(r))
	}
}

func openURL(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	_ = cmd.Start()
}

// revealInFolder opens the OS file manager at the given path's directory.
func revealInFolder(path string) {
	dir := filepath.Dir(path)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", dir)
	case "windows":
		cmd = exec.Command("explorer", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	_ = cmd.Start()
}

func miniscramVersion() string {
	out, err := exec.Command("miniscram", "--version").Output()
	if err != nil {
		return "unknown"
	}
	v := strings.TrimSpace(string(out))
	v = strings.TrimPrefix(v, "miniscram ")
	return v
}

// ---------------- palette ----------------

var (
	bg       = mustRGB("101216")
	surface  = mustRGB("1a1d24")
	surface2 = mustRGB("23262e")
	line     = mustRGB("2c313a")
	text1    = mustRGB("eaedf2")
	text2    = mustRGB("a6aebb")
	text3    = mustRGB("6f7682")
	accent   = mustRGB("7ce0c1")
	accentFg = mustRGB("07261c")
	warn     = mustRGB("f0a868")
	good     = mustRGB("7bd88f")
	bad      = mustRGB("ec6a6a")
	pending  = mustRGB("656972")
)

func mustRGB(s string) color.NRGBA {
	var r, g, b uint32
	fmt.Sscanf(s, "%02x%02x%02x", &r, &g, &b)
	return color.NRGBA{R: byte(r), G: byte(g), B: byte(b), A: 0xff}
}

// ---------------- main / loop ----------------

func main() {
	loadPath := flag.String("load", "", "auto-load a file (for screenshots)")
	startView := flag.String("view", "file", "starting view: file | stats")
	doSeed := flag.Bool("seed", false, "seed events table with example data and exit")
	mockRunning := flag.String("mock-running", "", "screenshot-only: inject a fake in-flight action ('pack'|'unpack'|'verify')")
	mockToast := flag.String("mock-toast", "", "screenshot-only: inject a fake success toast ('pack'|'unpack'|'verify')")
	flag.Parse()

	db, err := dbOpen()
	if err != nil {
		fmt.Fprintln(os.Stderr, "db open:", err)
		os.Exit(1)
	}

	if *doSeed {
		seedEvents(db)
		fmt.Println("seeded")
		os.Exit(0)
	}

	mdl := &model{
		db:               db,
		view:             *startView,
		redump:           map[string]*redumpEntry{},
		miniscramVersion: miniscramVersion(),
		modeHover:        map[int]*modeHover{},
	}
	if *loadPath != "" {
		mdl.load(*loadPath)
	}
	if mdl.view == "stats" {
		mdl.refreshStats()
	}

	mdl.runner = newActionRunner(func() {
		if mdl.invalidate != nil {
			mdl.invalidate()
		}
	})

	mdl.queue = newQueueModel()

	// Screenshot-only state injection. Same package, so direct field access.
	if *mockRunning != "" {
		mdl.runner.state = &runningState{
			Action:    *mockRunning,
			Input:     mdl.path,
			StartedAt: time.Now().Add(-7 * time.Second),
			LastLine:  "applying delta ... 4521000 byte(s) of delta applied",
		}
	}
	if *mockToast != "" {
		out := mdl.path
		switch *mockToast {
		case "pack":
			out = strings.TrimSuffix(mdl.path, filepath.Ext(mdl.path)) + ".miniscram"
		case "unpack":
			out = strings.TrimSuffix(mdl.path, filepath.Ext(mdl.path)) + ".scram"
		case "verify":
			out = ""
		}
		var size int64
		if out != "" {
			if st, err := os.Stat(out); err == nil {
				size = st.Size()
			}
		}
		mdl.toast = &toastState{
			Action:     *mockToast,
			Output:     out,
			OutputSize: size,
			DurationMs: 5230,
			ExpiresAt:  time.Now().Add(1 * time.Hour),
		}
	}

	go func() {
		w := new(app.Window)
		w.Option(app.Title("miniscram-gui"), app.Size(unit.Dp(1000), unit.Dp(820)))
		mdl.invalidate = func() { w.Invalidate() }
		if err := loop(w, mdl); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		os.Exit(0)
	}()
	app.Main()
}

type linkEntry struct {
	click *widget.Clickable
	url   string
}

func loop(w *app.Window, mdl *model) error {
	th := material.NewTheme()
	th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))
	th.Palette.Bg = bg
	th.Palette.Fg = text1

	var (
		openBtn         widget.Clickable
		statsBtn        widget.Clickable
		fileBtn         widget.Clickable
		verifyBtn       widget.Clickable
		unpackBtn       widget.Clickable
		packBtn         widget.Clickable
		cancelBtn       widget.Clickable
		toastDismissBtn widget.Clickable
		toastRevealBtn  widget.Clickable
		deleteScramCB   = widget.Bool{Value: true} // default: consume scram on success
		mockHoverCB     widget.Bool                // for screenshots
		copyBtns        = make(map[string]*widget.Clickable)
		linkBtns        = make(map[string]*linkEntry)
		listScroll      widget.List
	)
	_ = mockHoverCB
	listScroll.Axis = layout.Vertical
	getCopy := func(key string) *widget.Clickable {
		if c, ok := copyBtns[key]; ok {
			return c
		}
		c := new(widget.Clickable)
		copyBtns[key] = c
		return c
	}
	getLink := func(key, u string) *linkEntry {
		if e, ok := linkBtns[key]; ok {
			e.url = u
			return e
		}
		e := &linkEntry{click: new(widget.Clickable), url: u}
		linkBtns[key] = e
		return e
	}

	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			if mdl.runner != nil && mdl.runner.Running() {
				mdl.runner.Cancel()
				deadline := time.Now().Add(5 * time.Second)
				for mdl.runner.Running() && time.Now().Before(deadline) {
					time.Sleep(50 * time.Millisecond)
				}
			}
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)

			// Drain any completed action onto the UI goroutine. Single-flight
			// + cap-1 channel + r.invalidate() in wait() means at most one
			// result is ever pending, so a single non-blocking receive suffices.
			select {
			case res := <-mdl.runner.done:
				mdl.handleActionResult(res)
			default:
			}

			if statsBtn.Clicked(gtx) {
				mdl.view = "stats"
				mdl.refreshStats()
			}
			if fileBtn.Clicked(gtx) {
				mdl.view = "file"
			}
			if openBtn.Clicked(gtx) {
				go func() {
					p, err := pickFile()
					if err != nil || p == "" {
						return
					}
					mdl.load(p)
					if mdl.invalidate != nil {
						mdl.invalidate()
					}
				}()
			}
			if cancelBtn.Clicked(gtx) {
				mdl.runner.Cancel()
			}
			if verifyBtn.Clicked(gtx) && mdl.kind == "miniscram" && !mdl.runner.Running() {
				mdl.toast = nil
				_ = mdl.runner.Start("verify", mdl.path, "", "verify", "--progress=json", mdl.path)
			}
			if unpackBtn.Clicked(gtx) && mdl.kind == "miniscram" && !mdl.runner.Running() {
				mdl.toast = nil
				srcPath := mdl.path
				defaultName := strings.TrimSuffix(mdl.basename, filepath.Ext(mdl.basename)) + ".scram"
				defaultDir := mdl.dir
				go func() {
					out, err := pickSave(defaultName, defaultDir)
					if err != nil || out == "" {
						return
					}
					if out == srcPath {
						eventInsert(mdl.db, eventRec{
							TS:        time.Now(),
							Action:    "unpack",
							InputPath: srcPath,
							Status:    "fail",
							Error:     "refused: output path equals source .miniscram",
						})
						mdl.refreshStats()
						if mdl.invalidate != nil {
							mdl.invalidate()
						}
						return
					}
					_ = mdl.runner.Start("unpack", srcPath, out, "unpack", "--progress=json", srcPath, "-o", out)
				}()
			}
			if toastDismissBtn.Clicked(gtx) && mdl.toast != nil {
				mdl.toast.Hide = true
			}
			if toastRevealBtn.Clicked(gtx) && mdl.toast != nil && mdl.toast.Output != "" {
				revealInFolder(mdl.toast.Output)
			}
			if packBtn.Clicked(gtx) && mdl.kind == "cue" && !mdl.runner.Running() {
				mdl.toast = nil
				out := strings.TrimSuffix(mdl.path, filepath.Ext(mdl.path)) + ".miniscram"
				args := []string{"pack", "--progress=json", mdl.path}
				if !deleteScramCB.Value {
					args = append(args, "--keep-source")
				}
				_ = mdl.runner.Start("pack", mdl.path, out, args...)
			}
			for _, le := range linkBtns {
				if le.click.Clicked(gtx) && le.url != "" {
					openURL(le.url)
				}
			}
			// per-row delete buttons in the stats view
			for id, btn := range mdl.deleteBtns {
				if btn.Clicked(gtx) {
					eventDelete(mdl.db, id)
					delete(mdl.deleteBtns, id)
					mdl.refreshStats()
				}
			}

			paint.Fill(gtx.Ops, bg)

			layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return topBar(th, mdl, &openBtn, &statsBtn, &fileBtn).Layout(gtx)
				}),
				layout.Rigid(divider),
				layout.Rigid(runningStripWidget(th, mdl.runner.Snapshot(), &cancelBtn)),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return material.List(th, &listScroll).Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions {
						return layout.UniformInset(unit.Dp(24)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							switch mdl.view {
							case "stats":
								return statsView(gtx, th, mdl)
							default:
								return body(gtx, th, mdl, &verifyBtn, &unpackBtn, &packBtn, &deleteScramCB, getCopy, getLink)
							}
						})
					})
				}),
				layout.Rigid(toastWidget(th, mdl.toast, &toastDismissBtn, &toastRevealBtn)),
				layout.Rigid(divider),
				layout.Rigid(footer(th, mdl)),
			)

			e.Frame(gtx.Ops)
		}
	}
}

// ---------------- top bar ----------------

func topBar(th *material.Theme, mdl *model, openBtn, statsBtn, fileBtn *widget.Clickable) topBarStyle {
	return topBarStyle{th: th, mdl: mdl, openBtn: openBtn, statsBtn: statsBtn, fileBtn: fileBtn}
}

type topBarStyle struct {
	th                         *material.Theme
	mdl                        *model
	openBtn, statsBtn, fileBtn *widget.Clickable
}

func (b topBarStyle) Layout(gtx layout.Context) layout.Dimensions {
	return layout.Inset{Top: unit.Dp(14), Bottom: unit.Dp(14), Left: unit.Dp(24), Right: unit.Dp(24)}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					title := material.Label(b.th, unit.Sp(20), "miniscram")
					title.Font.Weight = font.Bold
					title.Color = text1
					return title.Layout(gtx)
				}),
				layout.Rigid(spacer(20, 0)),
				layout.Rigid(tabButton(b.th, b.fileBtn, "Inspect", b.mdl.view == "file")),
				layout.Rigid(spacer(4, 0)),
				layout.Rigid(tabButton(b.th, b.statsBtn, "Stats", b.mdl.view == "stats")),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					btn := material.Button(b.th, b.openBtn, "Open file…")
					btn.Background = surface2
					btn.Color = text1
					btn.CornerRadius = unit.Dp(6)
					btn.TextSize = unit.Sp(13)
					btn.Inset = layout.Inset{Top: 8, Bottom: 8, Left: 14, Right: 14}
					return btn.Layout(gtx)
				}),
			)
		})
}

func tabButton(th *material.Theme, c *widget.Clickable, label string, active bool) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		btn := material.Button(th, c, label)
		if active {
			btn.Background = surface2
			btn.Color = text1
		} else {
			btn.Background = bg
			btn.Color = text3
		}
		btn.CornerRadius = unit.Dp(6)
		btn.TextSize = unit.Sp(13)
		btn.Inset = layout.Inset{Top: 6, Bottom: 6, Left: 12, Right: 12}
		return btn.Layout(gtx)
	}
}

// ---------------- file view ----------------

func body(gtx layout.Context, th *material.Theme,
	mdl *model,
	verifyBtn, unpackBtn, packBtn *widget.Clickable,
	deleteScram *widget.Bool,
	getCopy func(string) *widget.Clickable,
	getLink func(string, string) *linkEntry,
) layout.Dimensions {
	switch mdl.kind {
	case "miniscram":
		return miniscramView(gtx, th, mdl, verifyBtn, unpackBtn, getCopy, getLink)
	case "cue":
		return cueView(gtx, th, mdl, packBtn, deleteScram)
	default:
		return emptyView(gtx, th, mdl)
	}
}

func miniscramView(gtx layout.Context, th *material.Theme, mdl *model,
	verifyBtn, unpackBtn *widget.Clickable,
	getCopy func(string) *widget.Clickable,
	getLink func(string, string) *linkEntry,
) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(filePathRow(th, mdl)),
		layout.Rigid(spacer(0, 16)),
		layout.Rigid(heroRow(th, mdl, verifyBtn, unpackBtn)),
		layout.Rigid(spacer(0, 16)),
		layout.Rigid(statTilesRow(th, mdl)),
		layout.Rigid(spacer(0, 16)),
		layout.Rigid(tracksCard(th, mdl, getCopy, getLink)),
		layout.Rigid(spacer(0, 16)),
		layout.Rigid(scramHashesCard(th, mdl, getCopy)),
	)
}

func cueView(gtx layout.Context, th *material.Theme, mdl *model, packBtn *widget.Clickable, deleteScram *widget.Bool) layout.Dimensions {
	scramPath := strings.TrimSuffix(mdl.path, filepath.Ext(mdl.path)) + ".scram"
	hasScram := false
	if st, err := os.Stat(scramPath); err == nil && st.Size() > 0 {
		hasScram = true
	}
	allBinsExist := true
	var totalBin int64
	for _, ct := range mdl.cueTracks {
		if !ct.exists {
			allBinsExist = false
		}
		totalBin += ct.size
	}

	statusText := "Ready to pack"
	statusCol := good
	if !hasScram {
		statusText = "Missing .scram next to cue — pack can't run"
		statusCol = warn
	} else if !allBinsExist {
		statusText = "One or more .bin files referenced by the cue are missing"
		statusCol = warn
	}

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(filePathRow(th, mdl)),
		layout.Rigid(spacer(0, 16)),
		layout.Rigid(card(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return statusDot(gtx, statusCol) }),
				layout.Rigid(spacer(12, 0)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(th, unit.Sp(15), statusText)
					l.Color = text1
					return l.Layout(gtx)
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(th, unit.Sp(12), fmt.Sprintf("%d track(s) · %s total", len(mdl.cueTracks), humanBytes(totalBin)))
					l.Color = text3
					return l.Layout(gtx)
				}),
				layout.Rigid(spacer(16, 0)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if !hasScram || !allBinsExist || mdl.runner.Running() {
						gtx = gtx.Disabled()
					}
					btn := material.Button(th, packBtn, "Pack")
					btn.Background = accent
					btn.Color = accentFg
					btn.CornerRadius = unit.Dp(6)
					btn.TextSize = unit.Sp(14)
					btn.Inset = layout.Inset{Top: 10, Bottom: 10, Left: 22, Right: 22}
					btn.Font.Weight = font.SemiBold
					if !hasScram || !allBinsExist || mdl.runner.Running() {
						btn.Background = surface2
						btn.Color = text3
					}
					return btn.Layout(gtx)
				}),
			)
		})),
		layout.Rigid(spacer(0, 8)),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				cb := material.CheckBox(th, deleteScram, "Delete the source .scram after a verified round-trip")
				cb.Color = text2
				cb.IconColor = accent
				cb.TextSize = unit.Sp(12)
				cb.Size = unit.Dp(16)
				return cb.Layout(gtx)
			})
		}),
		layout.Rigid(spacer(0, 16)),
		layout.Rigid(cueTracksCard(th, mdl)),
	)
}

func emptyView(gtx layout.Context, th *material.Theme, mdl *model) layout.Dimensions {
	msg := "Drop a .miniscram or .cue here, or use Open file…"
	if mdl.err != "" {
		msg = "error: " + mdl.err
	}
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(th, unit.Sp(15), msg)
				l.Color = text2
				return l.Layout(gtx)
			}),
		)
	})
}

// ---------------- stats view ----------------

func statsView(gtx layout.Context, th *material.Theme, mdl *model) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := material.Label(th, unit.Sp(20), "Stats")
			l.Color = text1
			l.Font.Weight = font.SemiBold
			return l.Layout(gtx)
		}),
		layout.Rigid(spacer(0, 4)),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := material.Label(th, unit.Sp(12), "Aggregated from local pack/unpack/verify history.")
			l.Color = text3
			return l.Layout(gtx)
		}),
		layout.Rigid(spacer(0, 16)),
		layout.Rigid(statsTilesRow(th, mdl)),
		layout.Rigid(spacer(0, 16)),
		layout.Rigid(eventsTable(th, mdl, mdl.getDeleteBtn)),
	)
}

func statsTilesRow(th *material.Theme, mdl *model) layout.Widget {
	a := mdl.stats
	tiles := []struct{ k, v, sub string }{
		{"packs", fmt.Sprintf("%d", a.PackOps), fmt.Sprintf("%d total operations", a.TotalOps)},
		{"bytes saved", humanBytes(a.TotalSavedBytes), "scram → miniscram"},
		{"best ratio", ratioFloatOrDash(a.BestRatio), a.BestRatioTitle},
		{"override records", fmt.Sprintf("%d", a.OverrideTotal), "across all packs"},
	}
	return func(gtx layout.Context) layout.Dimensions {
		var children []layout.FlexChild
		for i, t := range tiles {
			t := t
			if i > 0 {
				children = append(children, layout.Rigid(spacer(12, 0)))
			}
			children = append(children, layout.Flexed(1, statTileWithSub(th, t.k, t.v, t.sub)))
		}
		return layout.Flex{}.Layout(gtx, children...)
	}
}

func statTileWithSub(th *material.Theme, label, value, sub string) layout.Widget {
	return card(func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(th, unit.Sp(10), strings.ToUpper(label))
				l.Color = text3
				return l.Layout(gtx)
			}),
			layout.Rigid(spacer(0, 6)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(th, unit.Sp(20), value)
				l.Color = text1
				l.Font.Typeface = "Go Mono"
				l.Font.Weight = font.SemiBold
				return l.Layout(gtx)
			}),
			layout.Rigid(spacer(0, 4)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(th, unit.Sp(11), sub)
				l.Color = text3
				return l.Layout(gtx)
			}),
		)
	})
}

func ratioFloatOrDash(r float64) string {
	if r == 0 {
		return "—"
	}
	switch {
	case r >= 1_000_000:
		return fmt.Sprintf("%.2fM×", r/1_000_000)
	case r >= 1_000:
		return fmt.Sprintf("%.1fK×", r/1_000)
	default:
		return fmt.Sprintf("%.0f×", math.Round(r))
	}
}

func eventsTable(th *material.Theme, mdl *model, getDelete func(int64) *widget.Clickable) layout.Widget {
	return card(func(gtx layout.Context) layout.Dimensions {
		var children []layout.FlexChild
		children = append(children, layout.Rigid(sectionHeader(th, "Recent operations")))
		children = append(children, layout.Rigid(spacer(0, 12)))
		children = append(children, layout.Rigid(eventsHeaderRow(th)))
		children = append(children, layout.Rigid(thinDivider))
		if len(mdl.recent) == 0 {
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(12), Bottom: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					l := material.Label(th, unit.Sp(13), "No history yet — pack a disc to see it here.")
					l.Color = text3
					return l.Layout(gtx)
				})
			}))
		}
		for i, ev := range mdl.recent {
			i := i
			ev := ev
			if i > 0 {
				children = append(children, layout.Rigid(thinDivider))
			}
			children = append(children, layout.Rigid(eventRow(th, ev, getDelete(ev.ID))))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
}

func eventsHeaderRow(th *material.Theme) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Bottom: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx,
				cellHead(th, "WHEN", 110),
				cellHead(th, "ACTION", 80),
				layout.Flexed(2, headLabel(th, "TITLE / FILE")),
				cellHead(th, "RATIO", 90),
				cellHead(th, "SAVED", 110),
				cellHead(th, "STATUS", 80),
				cellHead(th, "", 50),
			)
		})
	}
}

func eventRow(th *material.Theme, ev eventRec, deleteBtn *widget.Clickable) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		when := timeAgo(ev.TS)
		title := ev.Title
		if title == "" {
			title = filepath.Base(ev.InputPath)
		}
		ratio := "—"
		saved := "—"
		if ev.ScramSize > 0 && ev.MiniscramSize > 0 {
			ratio = ratioFloat(ev.ScramSize, ev.MiniscramSize)
			saved = humanBytes(ev.ScramSize - ev.MiniscramSize)
		}
		statusCol := good
		statusLabel := "PASS"
		switch ev.Status {
		case "fail":
			statusCol = bad
			statusLabel = "FAIL"
		case "cancelled":
			statusCol = text3
			statusLabel = "CANCELLED"
		}
		return layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				cellMono(th, when, 110, text2),
				cellAction(th, ev.Action, 80),
				layout.Flexed(2, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							l := material.Label(th, unit.Sp(13), title)
							l.Color = text1
							return l.Layout(gtx)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							l := material.Label(th, unit.Sp(11), filepath.Base(ev.InputPath))
							l.Color = text3
							l.Font.Typeface = "Go Mono"
							return l.Layout(gtx)
						}),
					)
				}),
				cellMono(th, ratio, 90, text2),
				cellMono(th, saved, 110, text2),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Dp(unit.Dp(80))
					gtx.Constraints.Max.X = gtx.Dp(unit.Dp(80))
					return layout.Flex{}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return chip(gtx, th, statusLabel, statusBg(statusCol), statusCol)
						}),
					)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Dp(unit.Dp(50))
					gtx.Constraints.Max.X = gtx.Dp(unit.Dp(50))
					btn := material.Button(th, deleteBtn, "✕")
					btn.Background = bg
					btn.Color = text3
					btn.CornerRadius = unit.Dp(4)
					btn.TextSize = unit.Sp(13)
					btn.Inset = layout.Inset{Top: 4, Bottom: 4, Left: 8, Right: 8}
					return btn.Layout(gtx)
				}),
			)
		})
	}
}

func statusBg(c color.NRGBA) color.NRGBA {
	// derive a darker bg from the status colour
	return color.NRGBA{R: c.R / 5, G: c.G / 5, B: c.B / 5, A: 0xff}
}

func cellAction(th *material.Theme, action string, w int) layout.FlexChild {
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Dp(unit.Dp(w))
		gtx.Constraints.Max.X = gtx.Dp(unit.Dp(w))
		bg := surface2
		fg := text2
		switch action {
		case "pack":
			bg = mustRGB("17392d")
			fg = good
		case "unpack":
			bg = mustRGB("1c2e44")
			fg = mustRGB("69b1ff")
		case "verify":
			bg = mustRGB("3a2c1e")
			fg = warn
		}
		return layout.Flex{}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return chip(gtx, th, action, bg, fg)
			}),
		)
	})
}

func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// ---------------- file rows + cards (unchanged from the prior pass) ----------------

func filePathRow(th *material.Theme, mdl *model) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(th, unit.Sp(20), mdl.basename)
				l.Color = text1
				l.Font.Weight = font.SemiBold
				return l.Layout(gtx)
			}),
			layout.Rigid(spacer(0, 4)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(th, unit.Sp(12), mdl.dir)
				l.Color = text3
				l.Font.Typeface = "Go Mono"
				return l.Layout(gtx)
			}),
		)
	}
}

func heroRow(th *material.Theme, mdl *model, verifyBtn, unpackBtn *widget.Clickable) layout.Widget {
	return card(func(gtx layout.Context) layout.Dimensions {
		ratio := ratioStr(mdl.meta.Scram.Size, mdl.miniscramOnDisk)
		bytesLine := fmt.Sprintf("%s  →  %s", humanBytes(mdl.meta.Scram.Size), humanBytes(mdl.miniscramOnDisk))
		desc := fmt.Sprintf("MSCM v2 · %d track(s) · %d override record(s)",
			len(mdl.meta.Tracks), len(mdl.meta.DeltaRecords))

		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						l := material.Label(th, unit.Sp(48), ratio)
						l.Color = accent
						l.Font.Weight = font.Bold
						return l.Layout(gtx)
					}),
					layout.Rigid(spacer(0, 4)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						l := material.Label(th, unit.Sp(15), bytesLine)
						l.Color = text1
						l.Font.Typeface = "Go Mono"
						return l.Layout(gtx)
					}),
					layout.Rigid(spacer(0, 6)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						l := material.Label(th, unit.Sp(12), desc)
						l.Color = text3
						return l.Layout(gtx)
					}),
				)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if mdl.runner.Running() {
							gtx = gtx.Disabled()
						}
						btn := material.Button(th, verifyBtn, "Verify")
						btn.Background = accent
						btn.Color = accentFg
						btn.CornerRadius = unit.Dp(6)
						btn.TextSize = unit.Sp(14)
						btn.Inset = layout.Inset{Top: 10, Bottom: 10, Left: 22, Right: 22}
						btn.Font.Weight = font.SemiBold
						return btn.Layout(gtx)
					}),
					layout.Rigid(spacer(8, 0)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if mdl.runner.Running() {
							gtx = gtx.Disabled()
						}
						btn := material.Button(th, unpackBtn, "Unpack…")
						btn.Background = surface2
						btn.Color = text1
						btn.CornerRadius = unit.Dp(6)
						btn.TextSize = unit.Sp(14)
						btn.Inset = layout.Inset{Top: 10, Bottom: 10, Left: 18, Right: 18}
						return btn.Layout(gtx)
					}),
				)
			}),
		)
	})
}

func statTilesRow(th *material.Theme, mdl *model) layout.Widget {
	created := time.Unix(mdl.meta.CreatedUnix, 0).UTC().Format("2006-01-02")
	tiles := []struct{ k, v string }{
		{"scram size", humanBytes(mdl.meta.Scram.Size)},
		{"write offset", fmt.Sprintf("%+d B", mdl.meta.WriteOffsetBytes)},
		{"leadin LBA", fmt.Sprintf("%d", mdl.meta.LeadinLBA)},
		{"created (UTC)", created},
	}
	return func(gtx layout.Context) layout.Dimensions {
		flex := layout.Flex{}
		var children []layout.FlexChild
		for i, t := range tiles {
			t := t
			if i > 0 {
				children = append(children, layout.Rigid(spacer(12, 0)))
			}
			children = append(children, layout.Flexed(1, statTile(th, t.k, t.v)))
		}
		return flex.Layout(gtx, children...)
	}
}

func statTile(th *material.Theme, label, value string) layout.Widget {
	return card(func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(th, unit.Sp(10), strings.ToUpper(label))
				l.Color = text3
				return l.Layout(gtx)
			}),
			layout.Rigid(spacer(0, 6)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(th, unit.Sp(16), value)
				l.Color = text1
				l.Font.Typeface = "Go Mono"
				return l.Layout(gtx)
			}),
		)
	})
}

func tracksCard(th *material.Theme, mdl *model,
	getCopy func(string) *widget.Clickable,
	getLink func(string, string) *linkEntry,
) layout.Widget {
	return card(func(gtx layout.Context) layout.Dimensions {
		var children []layout.FlexChild
		children = append(children, layout.Rigid(sectionHeader(th, "Tracks")))
		children = append(children, layout.Rigid(spacer(0, 12)))
		children = append(children, layout.Rigid(trackHeaderRow(th)))
		children = append(children, layout.Rigid(thinDivider))

		for i, t := range mdl.meta.Tracks {
			i := i
			t := t
			if i > 0 {
				children = append(children, layout.Rigid(thinDivider))
			}
			hover := mdl.modeHover[t.Number]
			if hover == nil {
				hover = &modeHover{}
				mdl.modeHover[t.Number] = hover
			}
			children = append(children, layout.Rigid(trackRow(th, t.Number, t.Mode, t.FirstLBA, t.Size, t.Filename, hover)))

			for _, algo := range []string{"sha1", "md5"} {
				if v, ok := t.Hashes[algo]; ok && v != "" {
					algo := algo
					v := v
					var entry *redumpEntry
					if algo == "sha1" {
						mdl.redumpMu.Lock()
						entry = mdl.redump[v]
						mdl.redumpMu.Unlock()
					}
					children = append(children, layout.Rigid(hashSubRow(th, algo, v, entry,
						getCopy(fmt.Sprintf("t%d-%s", t.Number, algo)),
						getLink(fmt.Sprintf("t%d-%s-link", t.Number, algo), entryURL(entry)),
					)))
				}
			}
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
}

func entryURL(e *redumpEntry) string {
	if e == nil {
		return ""
	}
	return e.URL
}

func trackHeaderRow(th *material.Theme) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Bottom: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx,
				cellHead(th, "#", 30),
				cellHead(th, "MODE", 130),
				cellHead(th, "FIRST LBA", 110),
				cellHead(th, "SIZE", 110),
				layout.Flexed(1, headLabel(th, "FILENAME")),
			)
		})
	}
}

func trackRow(th *material.Theme, num int, mode string, firstLBA int, size int64, filename string, hover *modeHover) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				cellMono(th, fmt.Sprintf("%d", num), 30, text1),
				cellMode(th, mode, 130, hover),
				cellMono(th, fmt.Sprintf("%d", firstLBA), 110, text2),
				cellMono(th, humanBytes(size), 110, text2),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					l := material.Label(th, unit.Sp(13), filename)
					l.Color = text1
					return l.Layout(gtx)
				}),
			)
		})
	}
}

func hashSubRow(th *material.Theme, algo, value string,
	entry *redumpEntry, copyBtn *widget.Clickable, link *linkEntry,
) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		display := value
		if len(display) > 60 {
			display = display[:60] + "…"
		}
		hashCol := text2
		statusLabel := ""
		if entry != nil {
			switch entry.State {
			case "found":
				hashCol = good
				statusLabel = "✓ " + entry.Title
			case "miss":
				hashCol = bad
				statusLabel = "not on redump"
			case "pending":
				hashCol = pending
				statusLabel = "checking redump…"
			case "err":
				hashCol = warn
				statusLabel = "redump lookup failed"
			}
		}
		return layout.Inset{Left: unit.Dp(30), Top: unit.Dp(2), Bottom: unit.Dp(2)}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min.X = gtx.Dp(80)
						l := material.Label(th, unit.Sp(10), strings.ToUpper(algoDisplay(algo)))
						l.Color = text3
						return l.Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						l := material.Label(th, unit.Sp(11), display)
						l.Color = hashCol
						l.Font.Typeface = "Go Mono"
						return l.Layout(gtx)
					}),
					layout.Rigid(spacer(12, 0)),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						if statusLabel == "" {
							return layout.Dimensions{Size: gtx.Constraints.Min}
						}
						l := material.Label(th, unit.Sp(11), statusLabel)
						l.Color = hashCol
						return l.Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if entry == nil || entry.State != "found" || link == nil || link.url == "" {
							return layout.Dimensions{}
						}
						return linkButton(th, link, "Open ↗")(gtx)
					}),
					layout.Rigid(spacer(6, 0)),
					layout.Rigid(copyButton(th, copyBtn)),
				)
			})
	}
}

func scramHashesCard(th *material.Theme, mdl *model, getCopy func(string) *widget.Clickable) layout.Widget {
	return card(func(gtx layout.Context) layout.Dimensions {
		var children []layout.FlexChild
		children = append(children, layout.Rigid(sectionHeader(th, "Original .scram hashes")))
		children = append(children, layout.Rigid(spacer(0, 8)))
		for i, algo := range []string{"sha256", "sha1", "md5"} {
			i := i
			algo := algo
			if i > 0 {
				children = append(children, layout.Rigid(thinDivider))
			}
			v := mdl.meta.Scram.Hashes[algo]
			children = append(children, layout.Rigid(scramHashRow(th, algo, v, getCopy("scram-"+algo))))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
}

func scramHashRow(th *material.Theme, algo, value string, copyBtn *widget.Clickable) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Dp(90)
					l := material.Label(th, unit.Sp(11), strings.ToUpper(algoDisplay(algo)))
					l.Color = text3
					return l.Layout(gtx)
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					l := material.Label(th, unit.Sp(12), value)
					l.Color = text1
					l.Font.Typeface = "Go Mono"
					return l.Layout(gtx)
				}),
				layout.Rigid(copyButton(th, copyBtn)),
			)
		})
	}
}

func cueTracksCard(th *material.Theme, mdl *model) layout.Widget {
	return card(func(gtx layout.Context) layout.Dimensions {
		var children []layout.FlexChild
		children = append(children, layout.Rigid(sectionHeader(th, fmt.Sprintf("Tracks in cue (%d)", len(mdl.cueTracks)))))
		children = append(children, layout.Rigid(spacer(0, 12)))
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx,
				cellHead(th, "#", 30),
				cellHead(th, "MODE", 140),
				cellHead(th, "SIZE", 110),
				layout.Flexed(1, headLabel(th, "FILE")),
			)
		}))
		children = append(children, layout.Rigid(thinDivider))
		for i, ct := range mdl.cueTracks {
			i := i
			ct := ct
			if i > 0 {
				children = append(children, layout.Rigid(thinDivider))
			}
			// hover state keyed by 1000+track to keep distinct from miniscram tracks
			hkey := 1000 + ct.num
			hover := mdl.modeHover[hkey]
			if hover == nil {
				hover = &modeHover{}
				mdl.modeHover[hkey] = hover
			}
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				sizeStr := humanBytes(ct.size)
				sizeCol := text2
				if !ct.exists {
					sizeStr = "missing"
					sizeCol = bad
				}
				return layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						cellMono(th, fmt.Sprintf("%d", ct.num), 30, text1),
						cellMode(th, ct.mode, 140, hover),
						cellMono(th, sizeStr, 110, sizeCol),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							l := material.Label(th, unit.Sp(13), ct.filename)
							l.Color = text1
							return l.Layout(gtx)
						}),
					)
				})
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
}

// ---------------- footer ----------------

func footer(th *material.Theme, mdl *model) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8), Left: unit.Dp(24), Right: unit.Dp(24)}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						l := material.Label(th, unit.Sp(11),
							fmt.Sprintf("miniscram-gui %s · miniscram %s", guiVersion, mdl.miniscramVersion))
						l.Color = text3
						return l.Layout(gtx)
					}),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						l := material.Label(th, unit.Sp(11), "redump.org checks via SHA-1")
						l.Color = text3
						return l.Layout(gtx)
					}),
				)
			})
	}
}

// ---------------- small reusable bits ----------------

func divider(gtx layout.Context) layout.Dimensions {
	rect := image.Rect(0, 0, gtx.Constraints.Max.X, gtx.Dp(unit.Dp(1)))
	defer clip.Rect(rect).Push(gtx.Ops).Pop()
	paint.ColorOp{Color: line}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	return layout.Dimensions{Size: rect.Size()}
}

func thinDivider(gtx layout.Context) layout.Dimensions {
	rect := image.Rect(0, 0, gtx.Constraints.Max.X, gtx.Dp(unit.Dp(1)))
	defer clip.Rect(rect).Push(gtx.Ops).Pop()
	paint.ColorOp{Color: line}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	return layout.Dimensions{Size: rect.Size()}
}

func spacer(w, h int) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Spacer{Width: unit.Dp(w), Height: unit.Dp(h)}.Layout(gtx)
	}
}

func chip(gtx layout.Context, th *material.Theme, label string, bg, fg color.NRGBA) layout.Dimensions {
	macro := op.Record(gtx.Ops)
	dims := layout.Inset{Top: 3, Bottom: 3, Left: 8, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		l := material.Label(th, unit.Sp(11), label)
		l.Color = fg
		l.Font.Typeface = "Go Mono"
		return l.Layout(gtx)
	})
	call := macro.Stop()
	rr := clip.RRect{Rect: image.Rect(0, 0, dims.Size.X, dims.Size.Y), SE: 4, NW: 4, NE: 4, SW: 4}
	paint.FillShape(gtx.Ops, bg, rr.Op(gtx.Ops))
	call.Add(gtx.Ops)
	return dims
}

func card(inner layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		macro := op.Record(gtx.Ops)
		dims := layout.UniformInset(unit.Dp(18)).Layout(gtx, inner)
		call := macro.Stop()
		rr := clip.RRect{Rect: image.Rect(0, 0, dims.Size.X, dims.Size.Y), SE: 8, NW: 8, NE: 8, SW: 8}
		paint.FillShape(gtx.Ops, surface, rr.Op(gtx.Ops))
		call.Add(gtx.Ops)
		return dims
	}
}

func sectionHeader(th *material.Theme, title string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		l := material.Label(th, unit.Sp(11), strings.ToUpper(title))
		l.Color = text3
		l.Font.Weight = font.SemiBold
		return l.Layout(gtx)
	}
}

func cellHead(th *material.Theme, label string, w int) layout.FlexChild {
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Dp(unit.Dp(w))
		gtx.Constraints.Max.X = gtx.Dp(unit.Dp(w))
		return headLabel(th, label)(gtx)
	})
}

func headLabel(th *material.Theme, label string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		l := material.Label(th, unit.Sp(10), strings.ToUpper(label))
		l.Color = text3
		return l.Layout(gtx)
	}
}

func cellMono(th *material.Theme, value string, w int, c color.NRGBA) layout.FlexChild {
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Dp(unit.Dp(w))
		gtx.Constraints.Max.X = gtx.Dp(unit.Dp(w))
		l := material.Label(th, unit.Sp(13), value)
		l.Color = c
		l.Font.Typeface = "Go Mono"
		return l.Layout(gtx)
	})
}

// cellMode reserves a column of width w and renders a mode chip at the
// natural width of its label inside that column. For data tracks the chip
// shows "DATA"; on hover the precise mode (MODE1/2352, MODE2/2352) replaces
// the label. AUDIO is always shown verbatim.
func cellMode(th *material.Theme, mode string, w int, hover *modeHover) layout.FlexChild {
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Dp(unit.Dp(w))
		gtx.Constraints.Max.X = gtx.Dp(unit.Dp(w))
		bg := surface2
		fg := text2
		label := "DATA"
		isAudio := mode == "AUDIO"
		if isAudio {
			bg = mustRGB("3a2c1e")
			fg = warn
			label = "AUDIO"
		}
		if hover != nil && hover.hovered && !isAudio {
			label = mode
		}
		return layout.Flex{}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				dims := chip(gtx, th, label, bg, fg)
				if hover != nil && !isAudio {
					defer clip.Rect{Max: dims.Size}.Push(gtx.Ops).Pop()
					event.Op(gtx.Ops, hover)
					for {
						ev, ok := gtx.Event(pointer.Filter{Target: hover, Kinds: pointer.Enter | pointer.Leave | pointer.Cancel})
						if !ok {
							break
						}
						if pe, ok := ev.(pointer.Event); ok {
							switch pe.Kind {
							case pointer.Enter:
								hover.hovered = true
							case pointer.Leave, pointer.Cancel:
								hover.hovered = false
							}
						}
					}
				}
				return dims
			}),
		)
	})
}

func copyButton(th *material.Theme, c *widget.Clickable) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		btn := material.Button(th, c, "Copy")
		btn.Background = surface2
		btn.Color = text2
		btn.CornerRadius = unit.Dp(4)
		btn.TextSize = unit.Sp(11)
		btn.Inset = layout.Inset{Top: 4, Bottom: 4, Left: 10, Right: 10}
		return btn.Layout(gtx)
	}
}

func linkButton(th *material.Theme, link *linkEntry, label string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		btn := material.Button(th, link.click, label)
		btn.Background = mustRGB("17392d")
		btn.Color = good
		btn.CornerRadius = unit.Dp(4)
		btn.TextSize = unit.Sp(11)
		btn.Inset = layout.Inset{Top: 4, Bottom: 4, Left: 10, Right: 10}
		return btn.Layout(gtx)
	}
}

func statusDot(gtx layout.Context, c color.NRGBA) layout.Dimensions {
	d := gtx.Dp(unit.Dp(10))
	r := clip.Ellipse{Max: image.Point{X: d, Y: d}}
	paint.FillShape(gtx.Ops, c, r.Op(gtx.Ops))
	return layout.Dimensions{Size: image.Point{X: d, Y: d}}
}

func algoDisplay(s string) string {
	switch s {
	case "sha256":
		return "SHA-256"
	case "sha1":
		return "SHA-1"
	case "md5":
		return "MD5"
	}
	return s
}
