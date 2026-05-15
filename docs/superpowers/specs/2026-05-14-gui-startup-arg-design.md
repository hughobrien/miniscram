# GUI: startup positional argument

Date: 2026-05-14

## Motivation

Today `miniscram-gui` only consumes named flags. The screenshot-only
`-load <file>` injects an inspect target at startup, but a user
who runs `miniscram-gui ~/redumper/dumps` from a shell (or drags a
folder onto the app) gets an empty GUI and has to click AddDir
inside the GUI to do what the shell command obviously meant.

A single positional argument should be treated as a startup target:
- A directory becomes the equivalent of clicking AddDir.
- A file becomes the equivalent of `-load <file>`.

## Design

### Behavior

A single positional argument is accepted at startup. The
`flag.Parse()` call already exposes positional args via
`flag.Args()`; this design uses the first one.

| State | Result |
|---|---|
| Neither `-load` nor positional | nothing (current behavior) |
| `-load <file>` only | `mdl.load(file)` (current behavior) |
| Positional resolves to a directory | `mdl.queue.addPaths(mdl, []string{p})` — equivalent of AddDir click |
| Positional resolves to a file (or stat fails) | `mdl.load(p)` |
| Both `-load` and positional | `-load` wins; positional ignored |
| Multiple positional args | only the first is used; the rest are ignored |

The stat-fails-fallthrough-to-load() arm is deliberate: `load()`
already sets `m.err` for unreadable paths and falls through to
`"drop a .miniscram or a .cue"` for unrecognized extensions. That
existing error surface is good enough; we do not need a new error
path. A user typo on a directory name (`miniscram-gui /typo/dir`,
no extension) will produce the "drop a .miniscram or a .cue"
message — slightly misleading but accurate enough.

### Code shape

Two changes in `tools/miniscram-gui/main.go`:

1. **New helper.** Pure function that picks the path and classifies
   it. Pure-ness keeps the unit tests trivial.

   ```go
   type startupAction struct {
       Kind string // "" | "dir" | "file"
       Path string
   }

   func resolveStartupAction(loadFlag string, args []string) startupAction {
       p := loadFlag
       if p == "" && len(args) > 0 {
           p = args[0]
       }
       if p == "" {
           return startupAction{}
       }
       if st, err := os.Stat(p); err == nil && st.IsDir() {
           return startupAction{Kind: "dir", Path: p}
       }
       return startupAction{Kind: "file", Path: p}
   }
   ```

2. **Integration in `main()`.** The existing `-load` block at
   ~line 973-975 is replaced by a switch on the helper. The
   `mdl.queue = newQueueModel()` line at ~980 moves up so that
   the dir branch can call `mdl.queue.addPaths` immediately.

   ```go
   mdl.queue = newQueueModel()

   switch a := resolveStartupAction(*loadPath, flag.Args()); a.Kind {
   case "dir":
       mdl.queue.addPaths(mdl, []string{a.Path})
   case "file":
       mdl.load(a.Path)
   }
   if mdl.view == "stats" {
       mdl.refreshStats()
   }
   ```

   The `if mdl.view == "stats" { mdl.refreshStats() }` block at
   the original location (line 976-978) is preserved verbatim;
   it just appears after the switch instead of after a bare
   `if *loadPath != ""`.

   Order safety: `mdl.queue = newQueueModel()` previously sat
   below `mdl.load(*loadPath)`. `load()` does not read or write
   `mdl.queue`, and `newQueueModel()` does not read or write any
   field `load()` touches. The relocation is safe.

### Tests

A new file `tools/miniscram-gui/startup_arg_test.go` exercises
`resolveStartupAction` directly. The helper is pure (one
`os.Stat` call against caller-provided paths), so the tests use
`t.TempDir()` fixtures and don't need the GUI model at all.

- **`TestResolveStartupAction_Empty`** — empty flag, empty args →
  `Kind == ""`.
- **`TestResolveStartupAction_LoadFlagOnly_File`** — flag set to a
  real file path, no positional → `Kind == "file"`, `Path` matches
  flag.
- **`TestResolveStartupAction_LoadFlagOnly_Dir`** — flag set to a
  real directory, no positional → `Kind == "dir"`, `Path` matches
  flag. (Symmetry: `-load` doesn't get a free pass on directories;
  the helper classifies it the same as a positional.)
- **`TestResolveStartupAction_DirPositional`** — single positional
  pointing to `t.TempDir()` → `Kind == "dir"`.
- **`TestResolveStartupAction_FilePositional`** — single positional
  pointing to a temp file → `Kind == "file"`.
- **`TestResolveStartupAction_NonexistentPositional`** — positional
  pointing to `<tmpdir>/does-not-exist` → `Kind == "file"`
  (fallthrough; `load()`'s standard error surface handles it).
- **`TestResolveStartupAction_LoadFlagWinsOverPositional`** — both
  set → result matches the flag's classification, positional is
  ignored.
- **`TestResolveStartupAction_FirstPositionalWins`** — multiple
  positional args → result matches the first one.

The wiring in `main()` is not unit-tested — it's three lines of
glue inside `main`, and the codebase has no harness for the
event loop. Manual verification covers it.

### Manual verification

Build the GUI:

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go build .
```

- `./miniscram-gui /path/to/redumper/dumps` → launches with the
  queue populated by walking the directory for `.cue` files.
- `./miniscram-gui /path/to/disc.cue` → launches with the cue
  loaded in inspect.
- `./miniscram-gui /typo/no/such/dir` → launches; inspect view
  shows the "drop a .miniscram or a .cue" message.
- `./miniscram-gui -load /a/b.cue /a/some-dir` → loads
  `/a/b.cue`; `/a/some-dir` is ignored.

## Out of scope

- Multi-arg handling (`miniscram-gui a.cue b.cue c.cue`). The
  first arg wins; the rest are ignored. If batch-from-CLI becomes
  a real ask we route them all through `addPaths` later — but
  that's a separate decision.
- Updating `-help`/usage to document the positional arg. The
  flag package's auto-generated usage line is left alone.
- README documentation.
- Combining `-load` with a positional dir in any way other than
  "flag wins."

## Risk

- **`-load` is currently documented as a screenshot-only flag**
  (its usage string says so). Promoting its semantics — by
  letting it accept directories — slightly broadens that. No
  behavior change for existing callers of `-load <file>`; just
  a new shape (`-load <dir>`) becomes valid. Acceptable.
- **Stat in startup path** runs synchronously before the window
  opens. The path is one user-provided string; `os.Stat` returns
  in milliseconds even on slow networked filesystems for a
  single call. No risk.
