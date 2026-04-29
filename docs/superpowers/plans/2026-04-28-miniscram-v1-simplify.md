# miniscram v1 simplify — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** DRY pass + format simplification + e2e-focused test reshape, shipped as miniscram 1.0.0.

**Architecture:** Per `docs/superpowers/specs/2026-04-28-miniscram-v1-simplify-design.md`. Container version byte goes to 0x01 (v1, fresh start). Manifest slims by ~60% (drops fields derivable from tracks[] or delta payload; nests hashes; lifts scrambler-table SHA into binary header). Builder, delta encoder, hash loop, sync-validation, and CLI flag-parsing all consolidate to one canonical version each. Tests reshape around a single fixture builder and an e2e-matrix test driver.

**Tech Stack:** Go (stdlib only). No new dependencies.

**Pre-flight:** `go test ./...` green on `main` at the start of every task. If it isn't, stop and find out why.

**Reading order for a fresh implementer:** read `TASKS.md` (project history), then the spec doc above, then this plan. The spec is the rationale; this plan is the recipe.

---

## Task 1: Format v1 — slim manifest + binary header

**Goal:** Container format goes to v1. Binary header gains scrambler-table SHA-256. Manifest drops 9 redundant fields and nests hashes. Tool version string bumps to `miniscram 1.0.0`.

**Files:**
- Modify: `manifest.go` (Manifest struct, container constants, WriteContainer, ReadContainer)
- Modify: `cue.go` (`Track` struct: nest hashes)
- Modify: `pack.go` (manifest construction, toolVersion)
- Modify: `unpack.go` (manifest field access)
- Modify: `verify.go` (manifest field access)
- Modify: `inspect.go` (formatHumanInspect, formatJSONInspect)
- Modify: `pack_test.go`, `unpack_test.go`, `verify_test.go`, `inspect_test.go`, `manifest_test.go` (any literal Manifest or Track construction)
- Modify: `e2e_redump_test.go` (only if it inspects manifest fields directly)

**Acceptance Criteria:**
- [ ] `containerVersion = byte(0x01)` and a fresh-pack container's 5th byte is 0x01.
- [ ] `expectedScrambleTableSHA256` (32 raw bytes) is written at offsets [5, 37) in every container; `ReadContainer` rejects mismatches with a sentinel error.
- [ ] Manifest JSON contains only the v1 fields (see spec §"Manifest body"). No `format_version`, `bin_*`, `delta_size`, `error_*`, or `scrambler_table_sha256` at top level.
- [ ] Per-entity hashes are nested as `"hashes": {"md5":..., "sha1":..., "sha256":...}` for `scram` and each track.
- [ ] `toolVersion = "miniscram 1.0.0"`.
- [ ] `go test ./...` green.

**Verify:** `go test ./...` and a manual round-trip against the synthetic fixture: `go run . pack testdata-tmp/x.cue && go run . inspect testdata-tmp/x.miniscram | head -20` (or use any existing test that does pack → inspect).

**Steps:**

- [ ] **Step 1: Define new container constants and helper**

In `manifest.go` replace the `containerMagic`/`containerVersion`/`errorSectorsListCap` block with:

```go
const (
    containerMagic   = "MSCM"
    containerVersion = byte(0x01) // v1
    // Header layout: 4 magic + 1 version + 32 scrambler_hash + 4 manifest_len.
    containerHeaderSize = 4 + 1 + 32 + 4
)

// errScramblerHashMismatch indicates the container's recorded scrambler-
// table SHA-256 doesn't match the one this build computes — i.e., the
// scrambler implementation has drifted from the version that wrote the
// container.
var errScramblerHashMismatch = errors.New("scrambler table hash mismatch")
```

`errorSectorsListCap` (currently in `manifest.go`) is still used by `builder.go` to cap the in-memory mismatch list returned to callers — *move* it to `builder.go` in this same step so the constant lives next to its sole consumer:

```go
// At the top of builder.go:
const errorSectorsListCap = 10000
```

(Just a relocation — the cap on the manifest's `error_sectors` array is gone because the array itself is gone.)

- [ ] **Step 2: Define new FileHashes type with JSON tags**

In `pack.go`, replace the existing `FileHashes` (struct without JSON tags) with:

```go
// FileHashes is the {md5, sha1, sha256} triple miniscram records per
// entity. Marshalled as a nested JSON object.
type FileHashes struct {
    MD5    string `json:"md5"`
    SHA1   string `json:"sha1"`
    SHA256 string `json:"sha256"`
}
```

The function bodies of `hashReader`, `hashFile`, `compareHashes` stay unchanged. While here, delete the duplicated stale doc comment on `hashReader` (lines 215-218 in current `pack.go`).

- [ ] **Step 3: Slim Track to use nested hashes**

In `cue.go`, replace the `Track` definition with:

```go
type Track struct {
    Number   int        `json:"number"`
    Mode     string     `json:"mode"`
    FirstLBA int32      `json:"first_lba"`
    Filename string     `json:"filename"`
    Size     int64      `json:"size"`
    Hashes   FileHashes `json:"hashes"`
}
```

`IsData()` is unchanged. Re-order is intentional — declaration order is also JSON-emit order.

- [ ] **Step 4: Slim Manifest type**

In `manifest.go`, replace the entire `Manifest` struct + `Marshal` with:

```go
// ScramInfo holds size + hashes for the .scram file.
type ScramInfo struct {
    Size   int64      `json:"size"`
    Hashes FileHashes `json:"hashes"`
}

// Manifest is the JSON metadata embedded in every v1 .miniscram container.
type Manifest struct {
    ToolVersion      string    `json:"tool_version"`
    CreatedUTC       string    `json:"created_utc"`
    WriteOffsetBytes int       `json:"write_offset_bytes"`
    LeadinLBA        int32     `json:"leadin_lba"`
    Scram            ScramInfo `json:"scram"`
    Tracks           []Track   `json:"tracks"`
}

// Marshal returns the JSON encoding.
func (m *Manifest) Marshal() ([]byte, error) {
    return json.Marshal(m)
}

// BinSize returns the total .bin size as the sum of per-track sizes.
// Replaces the v0.2 manifest field of the same name.
func (m *Manifest) BinSize() int64 {
    var n int64
    for _, t := range m.Tracks {
        n += t.Size
    }
    return n
}

// BinFirstLBA returns tracks[0].FirstLBA — i.e. where the .bin's data
// track starts on disc. Replaces the v0.2 manifest field.
func (m *Manifest) BinFirstLBA() int32 {
    if len(m.Tracks) == 0 {
        return 0
    }
    return m.Tracks[0].FirstLBA
}

// BinSectorCount returns BinSize() / SectorSize.
func (m *Manifest) BinSectorCount() int32 {
    return int32(m.BinSize() / int64(SectorSize))
}
```

- [ ] **Step 5: Update WriteContainer for the new header**

Replace `WriteContainer` body in `manifest.go`:

```go
func WriteContainer(path string, m *Manifest, deltaSrc io.Reader) error {
    body, err := m.Marshal()
    if err != nil {
        return err
    }
    tableHash, err := hex.DecodeString(expectedScrambleTableSHA256)
    if err != nil {
        return fmt.Errorf("decoding expected scrambler hash: %w", err)
    }
    if len(tableHash) != 32 {
        return fmt.Errorf("scrambler hash must be 32 bytes, got %d", len(tableHash))
    }
    tmp := path + ".tmp"
    f, err := os.Create(tmp)
    if err != nil {
        return err
    }
    closed := false
    defer func() {
        if !closed {
            _ = f.Close()
            _ = os.Remove(tmp)
        }
    }()
    if _, err := f.Write([]byte(containerMagic)); err != nil {
        return err
    }
    if _, err := f.Write([]byte{containerVersion}); err != nil {
        return err
    }
    if _, err := f.Write(tableHash); err != nil {
        return err
    }
    if err := binary.Write(f, binary.BigEndian, uint32(len(body))); err != nil {
        return err
    }
    if _, err := f.Write(body); err != nil {
        return err
    }
    if _, err := io.Copy(f, deltaSrc); err != nil {
        return err
    }
    if err := f.Sync(); err != nil {
        return err
    }
    if err := f.Close(); err != nil {
        return err
    }
    closed = true
    return os.Rename(tmp, path)
}
```

Add `"encoding/hex"` to the imports if not already present.

- [ ] **Step 6: Update ReadContainer for the new header**

Replace `ReadContainer` body in `manifest.go`:

```go
func ReadContainer(path string) (*Manifest, []byte, error) {
    f, err := os.Open(path)
    if err != nil {
        return nil, nil, err
    }
    defer f.Close()
    header := make([]byte, containerHeaderSize)
    if _, err := io.ReadFull(f, header); err != nil {
        return nil, nil, fmt.Errorf("reading container header: %w", err)
    }
    if string(header[:4]) != containerMagic {
        return nil, nil, fmt.Errorf("not a miniscram container (bad magic %q)", header[:4])
    }
    if header[4] != containerVersion {
        return nil, nil, fmt.Errorf("unsupported container version 0x%02x (this build expects 0x%02x)",
            header[4], containerVersion)
    }
    expectedHash, err := hex.DecodeString(expectedScrambleTableSHA256)
    if err != nil {
        return nil, nil, fmt.Errorf("decoding expected scrambler hash: %w", err)
    }
    if !bytes.Equal(header[5:37], expectedHash) {
        return nil, nil, fmt.Errorf("%w: container records %x, this build computes %x",
            errScramblerHashMismatch, header[5:37], expectedHash)
    }
    mlen := binary.BigEndian.Uint32(header[37:41])
    if mlen == 0 || mlen > 16<<20 {
        return nil, nil, fmt.Errorf("implausible manifest length %d", mlen)
    }
    body := make([]byte, mlen)
    if _, err := io.ReadFull(f, body); err != nil {
        return nil, nil, fmt.Errorf("reading manifest: %w", err)
    }
    var m Manifest
    if err := json.Unmarshal(body, &m); err != nil {
        return nil, nil, fmt.Errorf("parsing manifest JSON: %w", err)
    }
    delta, err := io.ReadAll(f)
    if err != nil {
        return nil, nil, err
    }
    return &m, delta, nil
}
```

Add `"bytes"` and `"encoding/hex"` to imports.

- [ ] **Step 7: Update Pack to construct the new manifest**

In `pack.go`, replace the manifest assembly block (steps 5-7 in current `Pack`) with the new shape. Key changes:

- Per-track hashes go into `tracks[i].Hashes` (no separate MD5/SHA1/SHA256 fields on Track).
- Manifest construction:

```go
m := &Manifest{
    ToolVersion:      toolVersion + " (" + runtime.Version() + ")",
    CreatedUTC:       time.Now().UTC().Format(time.RFC3339),
    WriteOffsetBytes: writeOffsetBytes,
    LeadinLBA:        opts.LeadinLBA,
    Scram: ScramInfo{
        Size:   scramSize,
        Hashes: scramHashes,
    },
    Tracks: tracks,
}
```

The `errSectors`, `binSize`, `binSectors`, `binHashes` locals computed earlier in Pack stop being assigned to manifest fields — they're either not needed (errSectors, since the delta carries the truth) or can be derived (`m.BinSize()`, `m.BinFirstLBA()`, `m.BinSectorCount()`).

Update `toolVersion` constant to `"miniscram 1.0.0"`.

Update Pack's track-hashing loop: each `tracks[i].Hashes = FileHashes{...}` instead of three separate fields.

- [ ] **Step 8: Update Unpack to consume the new manifest**

In `unpack.go`, replace every `m.Tracks[i].MD5` / `.SHA1` / `.SHA256` with `m.Tracks[i].Hashes.MD5` etc. Replace `m.BinMD5` / `m.BinSHA1` / `m.BinSHA256` with the rollup computed at unpack time (already happens — it's the `gotRoll` value). Delete the `wantRoll` construction since we no longer have a recorded roll-up to compare against; per-track hash equality is sufficient (a roll-up mismatch with all per-tracks matching is impossible).

Replace `m.ScramMD5` / `.ScramSHA1` / `.ScramSHA256` with `m.Scram.Hashes.MD5` etc.

Replace the size check `info.Size() != tr.Size` flow unchanged.

Replace the BuildParams construction:

```go
params := BuildParams{
    LeadinLBA:        m.LeadinLBA,
    WriteOffsetBytes: m.WriteOffsetBytes,
    ScramSize:        m.Scram.Size,
    BinFirstLBA:      m.BinFirstLBA(),
    BinSectorCount:   m.BinSectorCount(),
    Tracks:           m.Tracks,
}
```

Delete `deltaJSONSize` from the bottom of `unpack.go`. Replace its caller `st.Done("manifest %d bytes, delta %d bytes", deltaJSONSize(m), len(delta))` with `st.Done("delta %d bytes", len(delta))` (manifest size isn't load-bearing for the user).

- [ ] **Step 9: Update Verify to use the new manifest**

In `verify.go`, replace `wantHashes := FileHashes{MD5: m.ScramMD5, SHA1: m.ScramSHA1, SHA256: m.ScramSHA256}` with `wantHashes := m.Scram.Hashes`.

- [ ] **Step 10: Update inspect formatters**

In `inspect.go`'s `formatHumanInspect`, replace the manifest-field block with the v1 shape. Drop the `bin_*`, `delta_size`, `error_sector_count`, and `scrambler_table_sha256` lines (the table hash lives in the binary header now — emit it in the `container:` line):

```go
fmt.Fprintf(&b, "container:  %s v%d (scrambler %s…)\n",
    magic, version, hex.EncodeToString(scramblerHash[:6]))
b.WriteString("manifest:\n")
fmt.Fprintf(&b, "  tool_version:           %s\n", m.ToolVersion)
fmt.Fprintf(&b, "  created_utc:            %s\n", m.CreatedUTC)
fmt.Fprintf(&b, "  write_offset_bytes:     %d\n", m.WriteOffsetBytes)
fmt.Fprintf(&b, "  leadin_lba:             %d\n", m.LeadinLBA)
fmt.Fprintf(&b, "  scram.size:             %d\n", m.Scram.Size)
fmt.Fprintf(&b, "  scram.hashes.md5:       %s\n", m.Scram.Hashes.MD5)
fmt.Fprintf(&b, "  scram.hashes.sha1:      %s\n", m.Scram.Hashes.SHA1)
fmt.Fprintf(&b, "  scram.hashes.sha256:    %s\n", m.Scram.Hashes.SHA256)
```

The function signature must change to accept the scrambler hash (since it's no longer in the manifest):

```go
func formatHumanInspect(m *Manifest, magic string, version byte, scramblerHash [32]byte, delta []byte, full bool) (string, error)
```

Update the track-print loop to read from `t.Hashes` instead of `t.MD5/SHA1/SHA256`.

`runInspect` must read the scrambler hash from the container header. Easiest: change `ReadContainer` to also return `[32]byte` (the recorded hash). New signature:

```go
func ReadContainer(path string) (*Manifest, [32]byte, []byte, error)
```

Update every call site (`unpack.go`, `verify.go`, `pack.go`'s `verifyRoundTrip`, `inspect.go`). They all just discard the hash for now (`m, _, delta, err := ReadContainer(...)`) except inspect, which uses it.

For `formatJSONInspect`, replace the manifest-passthrough with a struct that nests the new shape. Since the manifest is already the right shape, just marshal it directly and splice `delta_records` on top. Replace `stableInspectFieldOrder` with a hand-built ordered output:

```go
func formatJSONInspect(m *Manifest, delta []byte) ([]byte, error) {
    type recordOut struct {
        ByteOffset uint64 `json:"byte_offset"`
        Length     uint32 `json:"length"`
        LBA        int64  `json:"lba"`
    }
    var records []recordOut
    if _, err := IterateDeltaRecords(delta, func(off uint64, length uint32) error {
        lba := int64(off)/int64(SectorSize) + int64(m.BinFirstLBA())
        records = append(records, recordOut{ByteOffset: off, Length: length, LBA: lba})
        return nil
    }); err != nil {
        return nil, err
    }
    if records == nil {
        records = []recordOut{}
    }
    type out struct {
        *Manifest
        DeltaRecords []recordOut `json:"delta_records"`
    }
    return json.Marshal(out{Manifest: m, DeltaRecords: records})
}
```

Delete `stableInspectFieldOrder`. The struct embedding gives stable field ordering for free.

- [ ] **Step 11: Update tests for the new manifest shape**

Every test that constructs a `Manifest{...}` literal or accesses old field names breaks compilation. Walk the test files in this order:

1. `manifest_test.go` — round-trip new manifest through WriteContainer/ReadContainer; add a test for scrambler-hash mismatch rejection.
2. `inspect_test.go` — `sampleManifest` returns the v1 shape; assertions check the new lines.
3. `pack_test.go` — `mustHashFile` left as-is for now (still useful), but assertions on `m.ScramSHA256` become `m.Scram.Hashes.SHA256`, etc.
4. `unpack_test.go` — same field-name updates.
5. `verify_test.go` — same.
6. `e2e_redump_test.go` — same.

The `sampleManifest()` helper in `inspect_test.go` becomes:

```go
func sampleManifest() *Manifest {
    return &Manifest{
        ToolVersion:      "miniscram 1.0.0 (go1.22)",
        CreatedUTC:       "2026-04-28T14:30:21Z",
        WriteOffsetBytes: -52,
        LeadinLBA:        -150,
        Scram: ScramInfo{
            Size: 739729728,
            Hashes: FileHashes{
                MD5:    strings.Repeat("1", 32),
                SHA1:   strings.Repeat("2", 40),
                SHA256: strings.Repeat("c", 64),
            },
        },
        Tracks: []Track{{
            Number:   1,
            Mode:     "MODE1/2352",
            FirstLBA: 0,
            Size:     235200,
            Filename: "x.bin",
            Hashes: FileHashes{
                MD5:    strings.Repeat("a", 32),
                SHA1:   strings.Repeat("b", 40),
                SHA256: strings.Repeat("d", 64),
            },
        }},
    }
}
```

Adjust assertions on inspect output to match the new line set (drop `bin_*`, `delta_size`, `error_sector_count`, `scrambler_table_sha256` checks; add `scram.hashes.*` checks).

For `inspect_test.go` calls to `formatHumanInspect`, supply a fake scrambler hash:

```go
var fakeHash [32]byte
copy(fakeHash[:], bytes.Repeat([]byte{0x88}, 32))
out, err := formatHumanInspect(m, "MSCM", 0x01, fakeHash, delta, false)
```

For ReadContainer call sites in tests, update from `m, _, err := ReadContainer(...)` to `m, _, _, err := ReadContainer(...)`.

- [ ] **Step 12: Run the suite**

```
go vet ./... && go test ./...
```

Both must succeed. Real-disc e2e tests (`e2e_redump_test.go`) use the build tag `redump_data` and only run when the fixtures exist; their compilation must still succeed:

```
go test -tags redump_data -run nothing ./...
```

(The `-run nothing` is just a build check.)

- [ ] **Step 13: Commit**

```bash
git add -A
git commit -m "$(cat <<'EOF'
v1: container format simplification

Bump container version byte to 0x01. Move scrambler-table SHA-256 into
the binary header (offsets [5..37)). Slim the manifest: drop
format_version, bin_*, error_*, delta_size, scrambler_table_sha256;
nest per-entity hashes as {md5,sha1,sha256} sub-objects.

Bump tool version string to miniscram 1.0.0.

This is a breaking format change. No files in the wild.
EOF
)"
```

---

## Task 2: hashTrackFiles helper

**Goal:** Extract the per-track + roll-up hashing loop into one helper used by Pack and Unpack.

**Files:**
- Modify: `pack.go` (extract helper, use it)
- Modify: `unpack.go` (use helper)

**Acceptance Criteria:**
- [ ] `hashTrackFiles(files []ResolvedFile) (perTrack []FileHashes, rollup FileHashes, err error)` exists in `pack.go`.
- [ ] Both Pack's and Unpack's per-track-hash-with-rollup loop is gone, replaced by a single call.
- [ ] Behavior identical to before: per-file hashes match, rollup matches.
- [ ] `go test ./...` green.

**Verify:** `go test ./...`. Specifically the e2e tests which exercise both Pack and Unpack.

**Steps:**

- [ ] **Step 1: Add hashTrackFiles**

In `pack.go` (after `hashFile`):

```go
// hashTrackFiles streams every file once, computing per-file MD5/SHA-1/
// SHA-256 hashes and a roll-up across all files in order. Used by Pack
// (to populate the manifest) and by Unpack (to verify against the
// manifest's recorded values). Files are read sequentially; failure on
// any file aborts.
func hashTrackFiles(files []ResolvedFile) ([]FileHashes, FileHashes, error) {
    rollM, rollS1, rollS256 := md5.New(), sha1.New(), sha256.New()
    rollW := io.MultiWriter(rollM, rollS1, rollS256)
    perFile := make([]FileHashes, len(files))
    for i, rf := range files {
        f, err := os.Open(rf.Path)
        if err != nil {
            return nil, FileHashes{}, err
        }
        m, s1, s256 := md5.New(), sha1.New(), sha256.New()
        w := io.MultiWriter(m, s1, s256, rollW)
        _, copyErr := io.Copy(w, f)
        f.Close()
        if copyErr != nil {
            return nil, FileHashes{}, copyErr
        }
        perFile[i] = FileHashes{
            MD5:    hex.EncodeToString(m.Sum(nil)),
            SHA1:   hex.EncodeToString(s1.Sum(nil)),
            SHA256: hex.EncodeToString(s256.Sum(nil)),
        }
    }
    rollup := FileHashes{
        MD5:    hex.EncodeToString(rollM.Sum(nil)),
        SHA1:   hex.EncodeToString(rollS1.Sum(nil)),
        SHA256: hex.EncodeToString(rollS256.Sum(nil)),
    }
    return perFile, rollup, nil
}
```

- [ ] **Step 2: Use it from Pack**

Replace the "single hashing pass over track files" block in `Pack` (currently lines ~100-126 of `pack.go`) with:

```go
st = r.Step("hashing tracks")
perTrack, _, err := hashTrackFiles(resolved.Files)
if err != nil {
    st.Fail(err)
    return err
}
for i := range tracks {
    tracks[i].Hashes = perTrack[i]
}
st.Done("%d track(s) hashed", len(tracks))
```

The roll-up is no longer stored anywhere (the manifest doesn't have a `bin_*` field anymore); discard it. If Unpack still needs to verify the rollup... see next step.

- [ ] **Step 3: Use it from Unpack**

Replace Unpack's "single hashing pass" block (currently lines ~75-115 of `unpack.go`) with:

```go
st = r.Step("verifying bin hashes")
perTrack, _, err := hashTrackFiles(files)
if err != nil {
    st.Fail(err)
    return err
}
for i, got := range perTrack {
    want := m.Tracks[i].Hashes
    if cmpErr := compareHashes(got, want); cmpErr != nil {
        err := fmt.Errorf("%w: track %d (%s): %v", errBinHashMismatch, m.Tracks[i].Number, m.Tracks[i].Filename, cmpErr)
        st.Fail(err)
        return err
    }
}
st.Done("all tracks match")
```

The rollup compare is gone — see Task 1 step 8 for rationale (per-track equality implies rollup equality).

- [ ] **Step 4: Test and commit**

```
go test ./...
git add pack.go unpack.go
git commit -m "extract hashTrackFiles, share between Pack and Unpack"
```

---

## Task 3: validateSyncCandidate helper

**Goal:** Extract sync-candidate validation (BCD + LBA decode + offset bounds) shared by `detectWriteOffset` and `checkConstantOffset`.

**Files:**
- Modify: `pack.go`

**Acceptance Criteria:**
- [ ] `validateSyncCandidate(f io.ReaderAt, syncOff int64, leadinLBA int32, scramSize int64) (writeOffset int, ok bool)` exists.
- [ ] `detectWriteOffset` calls it; the inline `tryCandidate` closure is gone.
- [ ] `checkConstantOffset` calls it; the inline `validateCandidate` closure is gone.
- [ ] Behavior identical: same syncs accepted, same syncs rejected. Existing tests cover this.
- [ ] `go test ./...` green.

**Verify:** `go test ./...` — `pack_test.go` and `e2e_redump_test.go` exercise both code paths.

**Steps:**

- [ ] **Step 1: Add the helper**

In `pack.go` (above `detectWriteOffset`):

```go
// validateSyncCandidate reads the 3-byte MSF header at syncOff+SyncLen
// and returns the implied write offset if all checks pass:
//   - BCD-valid header bytes (each nibble ≤ 9 after descrambling)
//   - decoded LBA in [leadinLBA, 500_000]
//   - implied write offset sample-aligned (multiple of 4)
//   - implied write offset within ±2 sectors
//
// Used by both detectWriteOffset (first valid candidate wins) and
// checkConstantOffset (samples N candidates and asserts equality).
//
// Returns ok=false on any failure (bad read, BCD mismatch, etc.).
func validateSyncCandidate(f io.ReaderAt, syncOff int64, leadinLBA int32, scramSize int64) (int, bool) {
    if syncOff+int64(SyncLen)+3 > scramSize {
        return 0, false
    }
    var header [3]byte
    if _, err := f.ReadAt(header[:], syncOff+int64(SyncLen)); err != nil {
        return 0, false
    }
    for i := 0; i < 3; i++ {
        header[i] ^= scrambleTable[SyncLen+i]
    }
    isBCD := func(b byte) bool { return (b>>4) <= 9 && (b&0x0F) <= 9 }
    if !isBCD(header[0]) || !isBCD(header[1]) || !isBCD(header[2]) {
        return 0, false
    }
    decodedLBA := BCDMSFToLBA(header)
    if decodedLBA < leadinLBA || decodedLBA > 500_000 {
        return 0, false
    }
    expectedAt := int64(decodedLBA-leadinLBA) * int64(SectorSize)
    writeOffset := int(syncOff - expectedAt)
    if writeOffset%4 != 0 {
        return 0, false
    }
    const writeOffsetLimit = 2 * SectorSize
    if writeOffset > writeOffsetLimit || writeOffset < -writeOffsetLimit {
        return 0, false
    }
    return writeOffset, true
}
```

- [ ] **Step 2: Refactor detectWriteOffset to use it**

In `pack.go`, replace the `tryCandidate` closure inside `detectWriteOffset` with a call:

```go
if wo, ok := validateSyncCandidate(f, syncOffset, leadinLBA, info.Size()); ok {
    return wo, nil
}
```

Remove the now-unused `isBCD` closure and `writeOffsetLimit` const inside `detectWriteOffset`.

- [ ] **Step 3: Refactor checkConstantOffset to use it**

In `pack.go`, the `validateCandidate` closure inside `checkConstantOffset` becomes a direct call:

```go
findValidSyncFrom := func(startAt int64) (int, bool, error) {
    // ... search loop unchanged, except:
    if off, ok := validateSyncCandidate(f, syncOff, leadinLBA, scramSize); ok {
        return off, true, nil
    }
    // ...
}
```

Remove the inner `validateCandidate` closure entirely. Keep the chunked-scan logic.

- [ ] **Step 4: Test and commit**

```
go test ./...
git add pack.go
git commit -m "extract validateSyncCandidate helper"
```

---

## Task 4: Unified DeltaEncoder + lift the per-record cap

**Goal:** Single streaming `DeltaEncoder` type used by both `EncodeDelta` and the (soon-to-be) builder mismatch callback. Lift the per-record `length ≤ SectorSize` cap.

**Files:**
- Modify: `delta.go`

**Acceptance Criteria:**
- [ ] `DeltaEncoder` type exists with `New`, `Append(off int64, run []byte)`, and `Close() (count int, err error)` methods.
- [ ] `EncodeDelta` reimplemented in terms of `DeltaEncoder`.
- [ ] Per-record length ceiling is `scramSize` (caller-supplied) instead of `SectorSize`. `ApplyDelta` and `IterateDeltaRecords` validate against `MaxDeltaRecordLength = 1 << 30` (1 GiB sanity ceiling) since they don't know scramSize at parse time.
- [ ] Existing delta tests pass.
- [ ] `go test ./...` green.

**Verify:** `go test ./... -run Delta` then `go test ./...`.

**Steps:**

- [ ] **Step 1: Define the streaming encoder**

Replace the body of `delta.go` with (preserving the package + imports):

```go
package main

import (
    "encoding/binary"
    "fmt"
    "io"
)

// MaxDeltaRecordLength is the upper bound enforced by readers (Apply,
// Iterate). It's a sanity ceiling for framing-corruption detection;
// real records are bounded by scram size at write time.
const MaxDeltaRecordLength = 1 << 30 // 1 GiB

// DeltaEncoder writes delta records to an io.Writer in two phases:
//   1. Append() buffers records in memory.
//   2. Close() emits the count header followed by the buffered body.
//
// This shape lets the encoder be driven by streaming sources (the
// builder's mismatch callback) without the caller having to count
// records ahead of time.
type DeltaEncoder struct {
    out   io.Writer
    body  []byte
    count uint32
}

func NewDeltaEncoder(out io.Writer) *DeltaEncoder {
    return &DeltaEncoder{out: out}
}

// Append records that the byte run starting at off should be applied
// to the output during ApplyDelta.
func (e *DeltaEncoder) Append(off int64, run []byte) {
    if len(run) == 0 {
        return
    }
    var hdr [12]byte
    binary.BigEndian.PutUint64(hdr[:8], uint64(off))
    binary.BigEndian.PutUint32(hdr[8:], uint32(len(run)))
    e.body = append(e.body, hdr[:]...)
    e.body = append(e.body, run...)
    e.count++
}

// Close emits the count + body and returns the final count.
func (e *DeltaEncoder) Close() (int, error) {
    var hdr [4]byte
    binary.BigEndian.PutUint32(hdr[:], e.count)
    if _, err := e.out.Write(hdr[:]); err != nil {
        return 0, err
    }
    if _, err := e.out.Write(e.body); err != nil {
        return 0, err
    }
    return int(e.count), nil
}

// EncodeDelta walks epsilonHat and scram in lockstep, emitting one
// override record per contiguous mismatch run. Records can be of any
// length up to scramSize.
func EncodeDelta(out io.Writer, epsilonHat, scram io.Reader, scramSize int64) (int, error) {
    enc := NewDeltaEncoder(out)
    const chunk = 1 << 20
    hatBuf := make([]byte, chunk)
    scrBuf := make([]byte, chunk)
    var pos int64
    var run []byte
    var runStart int64
    for pos < scramSize {
        want := int64(chunk)
        if pos+want > scramSize {
            want = scramSize - pos
        }
        if _, err := io.ReadFull(epsilonHat, hatBuf[:want]); err != nil {
            return 0, fmt.Errorf("reading epsilonHat at %d: %w", pos, err)
        }
        if _, err := io.ReadFull(scram, scrBuf[:want]); err != nil {
            return 0, fmt.Errorf("reading scram at %d: %w", pos, err)
        }
        for i := int64(0); i < want; i++ {
            if hatBuf[i] != scrBuf[i] {
                if len(run) == 0 {
                    runStart = pos + i
                }
                run = append(run, scrBuf[i])
            } else if len(run) > 0 {
                enc.Append(runStart, run)
                run = run[:0]
            }
        }
        pos += want
    }
    if len(run) > 0 {
        enc.Append(runStart, run)
    }
    return enc.Close()
}

// ApplyDelta reads override records from delta and writes their
// payloads at the recorded offsets in out.
func ApplyDelta(out io.WriterAt, delta io.Reader) error {
    var hdr [4]byte
    if _, err := io.ReadFull(delta, hdr[:]); err != nil {
        return fmt.Errorf("reading override count: %w", err)
    }
    count := binary.BigEndian.Uint32(hdr[:])
    for i := uint32(0); i < count; i++ {
        var rec [12]byte
        if _, err := io.ReadFull(delta, rec[:]); err != nil {
            return fmt.Errorf("reading override %d header: %w", i, err)
        }
        offset := int64(binary.BigEndian.Uint64(rec[:8]))
        length := binary.BigEndian.Uint32(rec[8:])
        if length == 0 || length > MaxDeltaRecordLength {
            return fmt.Errorf("override %d has implausible length %d", i, length)
        }
        payload := make([]byte, length)
        if _, err := io.ReadFull(delta, payload); err != nil {
            return fmt.Errorf("reading override %d payload: %w", i, err)
        }
        if _, err := out.WriteAt(payload, offset); err != nil {
            return fmt.Errorf("writing override %d at %d: %w", i, offset, err)
        }
    }
    return nil
}

// IterateDeltaRecords walks the override records in delta, calling fn
// for each record's byte offset and length. fn is not given the
// payload bytes; consumers like inspect/verify use it to enumerate
// records without materializing payloads.
func IterateDeltaRecords(delta []byte, fn func(off uint64, length uint32) error) (uint32, error) {
    if len(delta) < 4 {
        return 0, fmt.Errorf("delta too short for override count (%d bytes)", len(delta))
    }
    count := binary.BigEndian.Uint32(delta[:4])
    pos := 4
    for i := uint32(0); i < count; i++ {
        if pos+12 > len(delta) {
            return i, fmt.Errorf("override %d header truncated at offset %d", i, pos)
        }
        off := binary.BigEndian.Uint64(delta[pos : pos+8])
        length := binary.BigEndian.Uint32(delta[pos+8 : pos+12])
        pos += 12
        if length == 0 || length > MaxDeltaRecordLength {
            return i, fmt.Errorf("override %d has implausible length %d", i, length)
        }
        if pos+int(length) > len(delta) {
            return i, fmt.Errorf("override %d payload truncated (need %d bytes at offset %d, have %d)",
                i, length, pos, len(delta)-pos)
        }
        if err := fn(off, length); err != nil {
            return i, err
        }
        pos += int(length)
    }
    return count, nil
}
```

The old `deltaChunkSize` const, the old `flush` closure, and the per-record SectorSize cap all go away.

- [ ] **Step 2: Update delta_test.go to reflect new ceilings**

Search for tests that assert the per-sector cap. Likely `TestApplyDeltaImplausibleLength` or similar — relax to assert that `MaxDeltaRecordLength + 1` is rejected.

- [ ] **Step 3: Test and commit**

```
go test ./...
git add delta.go delta_test.go
git commit -m "delta: streaming DeltaEncoder, lift per-sector cap"
```

---

## Task 5: Unify BuildEpsilonHat with onMismatch callback

**Goal:** Replace `BuildEpsilonHat` and `BuildEpsilonHatAndDelta` with one function. Layout-mismatch ratio check moves to a separate `CheckLayoutMismatch` helper.

**Files:**
- Modify: `builder.go`
- Modify: `pack.go` (call site)
- Modify: `unpack.go` (call site)

**Acceptance Criteria:**
- [ ] `builder.go` exports exactly one builder entry point: `BuildEpsilonHat(out, p, bin, scram, onMismatch)`.
- [ ] `BuildEpsilonHatAndDelta` is gone.
- [ ] `CheckLayoutMismatch(errLBAs []int32, mismatchedSectors int, totalDiscSectors int32) error` exists.
- [ ] Pack supplies a `DeltaEncoder.Append`-bound callback; Unpack passes nil.
- [ ] Behavior preserved: `e2e_redump_test.go` Freelancer round-trip stays byte-equal; clean disc round-trip stays byte-equal.
- [ ] `go test ./...` green.

**Verify:** `go test ./...`. If `redump_data` fixtures are present locally, also `go test -tags redump_data ./...`.

**Steps:**

- [ ] **Step 1: Replace builder.go's two functions with one**

Keep the helpers (`generateMode1ZeroSector`, `generateLeadoutSector`, `trackModeAt`, `scramFileOffset`, `LayoutMismatchError`, `BuildParams`) untouched. Replace both `BuildEpsilonHat` and `BuildEpsilonHatAndDelta` with one implementation:

```go
// BuildEpsilonHat writes the reconstructed scrambled image to out.
//
// If scram is non-nil, every byte written is compared against scram in
// lockstep. onMismatch (if non-nil) is invoked for each contiguous
// run of mismatching bytes, with the file offset of the run start and
// the *scram* bytes (so the caller can encode delta records).
//
// Returns the list of LBAs that contained at least one mismatch
// (capped at errorSectorsListCap) and the count of mismatched sectors
// (uncapped). The caller decides what to do with this — see
// CheckLayoutMismatch.
//
// scram == nil implies onMismatch is ignored. The function does not
// emit anything on its own beyond writing to out; callers that need
// a delta payload supply onMismatch via a DeltaEncoder.
func BuildEpsilonHat(
    out io.Writer,
    p BuildParams,
    bin io.Reader,
    scram io.Reader,
    onMismatch func(off int64, scramRun []byte),
) ([]int32, int, error) {
    if p.ScramSize <= 0 {
        return nil, 0, errors.New("ScramSize must be positive")
    }
    totalLBAs := TotalLBAs(p.ScramSize, p.WriteOffsetBytes)
    endLBA := p.LeadinLBA + totalLBAs

    var written int64
    if p.WriteOffsetBytes > 0 {
        zeros := make([]byte, p.WriteOffsetBytes)
        if _, err := out.Write(zeros); err != nil {
            return nil, 0, err
        }
        written = int64(p.WriteOffsetBytes)
    }
    skipFirst := 0
    if p.WriteOffsetBytes < 0 {
        skipFirst = -p.WriteOffsetBytes
    }

    binBuf := make([]byte, SectorSize)
    scramBuf := make([]byte, SectorSize)
    var errLBAs []int32
    var mismatchedSectors int
    var scramCur int64

    advanceScram := func(target int64) error {
        if scram == nil || target <= scramCur {
            return nil
        }
        if _, err := io.CopyN(io.Discard, scram, target-scramCur); err != nil {
            return fmt.Errorf("advancing scram to %d: %w", target, err)
        }
        scramCur = target
        return nil
    }

    var run []byte
    var runStart int64

    for lba := p.LeadinLBA; lba < endLBA; lba++ {
        var sec [SectorSize]byte
        switch {
        case lba < LBAPregapStart:
            // leadin: zeros
        case lba < p.BinFirstLBA:
            sec = generateMode1ZeroSector(lba)
        case lba < p.BinFirstLBA+p.BinSectorCount:
            if _, err := io.ReadFull(bin, binBuf); err != nil {
                return nil, 0, fmt.Errorf("reading bin LBA %d: %w", lba, err)
            }
            copy(sec[:], binBuf)
            if trackModeAt(p.Tracks, lba) != "AUDIO" {
                Scramble(&sec)
            }
        default:
            sec = generateLeadoutSector(lba)
        }

        secBytes := sec[:]
        if skipFirst > 0 {
            secBytes = secBytes[skipFirst:]
            skipFirst = 0
        }
        remain := p.ScramSize - written
        if int64(len(secBytes)) > remain {
            secBytes = secBytes[:remain]
        }
        hatStart := written
        if _, err := out.Write(secBytes); err != nil {
            return nil, 0, err
        }
        written += int64(len(secBytes))

        if scram != nil {
            if err := advanceScram(hatStart); err != nil {
                return nil, 0, err
            }
            if _, err := io.ReadFull(scram, scramBuf[:len(secBytes)]); err != nil {
                return nil, 0, fmt.Errorf("reading scram at %d: %w", hatStart, err)
            }
            scramCur = hatStart + int64(len(secBytes))

            sectorMismatch := false
            for i := 0; i < len(secBytes); i++ {
                if secBytes[i] != scramBuf[i] {
                    if len(run) == 0 {
                        runStart = hatStart + int64(i)
                    }
                    run = append(run, scramBuf[i])
                    sectorMismatch = true
                } else if len(run) > 0 {
                    if onMismatch != nil {
                        onMismatch(runStart, run)
                    }
                    run = run[:0]
                }
            }
            if sectorMismatch {
                mismatchedSectors++
                if len(errLBAs) < errorSectorsListCap {
                    errLBAs = append(errLBAs, lba)
                }
            }
        }
        if written >= p.ScramSize {
            break
        }
    }
    if scram != nil && len(run) > 0 && onMismatch != nil {
        onMismatch(runStart, run)
    }

    return errLBAs, mismatchedSectors, nil
}

// CheckLayoutMismatch returns *LayoutMismatchError when the mismatch
// ratio exceeds layoutMismatchAbortRatio. Callers that have a scram to
// compare against (Pack) run this; callers that don't (Unpack) skip it.
func CheckLayoutMismatch(errLBAs []int32, mismatchedSectors int, totalDiscSectors int32) error {
    if totalDiscSectors <= 0 {
        return nil
    }
    ratio := float64(mismatchedSectors) / float64(totalDiscSectors)
    if ratio <= layoutMismatchAbortRatio {
        return nil
    }
    head := errLBAs
    if len(head) > 10 {
        head = head[:10]
    }
    return &LayoutMismatchError{
        BinSectors:    totalDiscSectors,
        ErrorSectors:  head,
        MismatchRatio: ratio,
    }
}
```

(`errorSectorsListCap` already lives in `builder.go` after Task 1's relocation — no action here.)

- [ ] **Step 2: Update Pack's call site**

In `pack.go`'s `buildHatAndDelta`, replace the `BuildEpsilonHatAndDelta` call with:

```go
enc := NewDeltaEncoder(deltaFile)
errs, mismatched, err := BuildEpsilonHat(hatFile, params, binReader, scramFile, enc.Append)
hatFile.Sync()
deltaFile.Sync()
hatFile.Close()
if cerr := enc.Close(); err == nil && cerr != nil {
    err = cerr
}
deltaFile.Close()
if err == nil {
    err = CheckLayoutMismatch(errs, mismatched, p.BinSectorCount /* not quite — see note */)
}
```

Note: `CheckLayoutMismatch` takes `totalDiscSectors`, which is the total sector count across the whole disc (leadin to leadout), not just the bin range. The caller should compute it as `int32(endLBA - p.LeadinLBA)`. Compute `endLBA = p.LeadinLBA + TotalLBAs(p.ScramSize, p.WriteOffsetBytes)` at the call site:

```go
totalDisc := TotalLBAs(p.ScramSize, p.WriteOffsetBytes)
err = CheckLayoutMismatch(errs, mismatched, totalDisc)
```

The `buildHatAndDelta` return shape stays the same (returns hat path, delta path, errLBAs, deltaSize, err) — but the function is now thinner. After this task, `buildHatAndDelta` is essentially:

```go
func buildHatAndDelta(opts PackOptions, files []ResolvedFile, tracks []Track, scramSize int64, writeOffsetBytes int, binSectors int32) (string, string, []int32, int64, error) {
    // ... open temp files (unchanged)
    enc := NewDeltaEncoder(deltaFile)
    params := BuildParams{...}
    errs, mismatched, err := BuildEpsilonHat(hatFile, params, binReader, scramFile, enc.Append)
    if err == nil {
        if _, cerr := enc.Close(); cerr != nil { err = cerr }
    }
    // ... sync + close + stat (unchanged)
    if err == nil {
        totalDisc := TotalLBAs(scramSize, writeOffsetBytes)
        err = CheckLayoutMismatch(errs, mismatched, totalDisc)
    }
    return hatPath, deltaPath, errs, deltaInfo.Size(), err
}
```

- [ ] **Step 3: Update Unpack's call site**

In `unpack.go`'s build-ε̂ block, the call becomes:

```go
if _, _, err := BuildEpsilonHat(hatFile, params, binReader, nil, nil); err != nil {
    closeBin()
    hatFile.Close()
    st.Fail(err)
    return err
}
```

(Drop the layout-check; Unpack has no scram to compare against.)

- [ ] **Step 4: Update verifyRoundTrip in pack.go**

Currently `verifyRoundTrip` calls `BuildEpsilonHat(hatFile, params, binReader, nil)` with the v0.2 signature. Update to `BuildEpsilonHat(hatFile, params, binReader, nil, nil)`. (This whole function is deleted in Task 6, so it's a brief stop.)

- [ ] **Step 5: Test and commit**

```
go test ./...
git add builder.go pack.go unpack.go manifest.go
git commit -m "builder: unify BuildEpsilonHat with onMismatch callback"
```

---

## Task 6: Pack uses Verify for round-trip

**Goal:** Delete `verifyRoundTrip` from `pack.go`. Pack's verification step calls `Verify()` instead.

**Files:**
- Modify: `pack.go`
- Modify: `verify.go`

**Acceptance Criteria:**
- [ ] `verifyRoundTrip` gone from `pack.go`.
- [ ] Pack's verification step is a one-line call to `Verify`.
- [ ] All existing pack tests still green; verify-failure-deletes-output behavior preserved.
- [ ] `go test ./...` green.

**Verify:** `go test ./... -run Pack` then `go test ./...`.

**Steps:**

- [ ] **Step 1: Update Pack to call Verify**

Replace the verify-roundtrip block at the end of `Pack` (currently around lines 193-205) with:

```go
if !opts.Verify {
    r.Warn("verification skipped (--no-verify)")
    return nil
}
if err := Verify(VerifyOptions{ContainerPath: opts.OutputPath}, r); err != nil {
    _ = os.Remove(opts.OutputPath)
    return fmt.Errorf("%w: %v", errVerifyMismatch, err)
}
return nil
```

The `errVerifyMismatch` wrap matches the v0.2 behavior (Pack errors with errVerifyMismatch on round-trip failure). Pack's exit code mapping then routes to `exitVerifyFail` via `errToExit`.

- [ ] **Step 2: Delete verifyRoundTrip**

Remove the entire `verifyRoundTrip` function (currently lines ~565-635 of `pack.go`). Tidy any imports it leaves orphaned (`bytes`, possibly).

- [ ] **Step 3: Test and commit**

```
go test ./...
git add pack.go
git commit -m "pack: round-trip verification calls Verify, not bespoke duplicate"
```

---

## Task 7: CLI consolidation

**Goal:** Single `parseSubcommand` flag-parser; single `errToExit`; single `printHelp`. Delete `filepathDir` (use `filepath.Dir`).

**Files:**
- Modify: `main.go`
- Modify: `help.go`

**Acceptance Criteria:**
- [ ] `parseSubcommand` exists and is used by all four `runX` functions.
- [ ] Single `errToExit(err)` function replaces `packErrorToExit`/`unpackErrorToExit`/`verifyErrorToExit`.
- [ ] Single `printHelp(w io.Writer, text string)` replaces five `printXHelp` functions.
- [ ] `filepathDir` deleted; `sameFilesystem` uses `filepath.Dir`.
- [ ] CLI behavior unchanged: same flags, same exit codes, same help output.
- [ ] `go test ./...` green; the existing `main_test.go` exit-code tests still pass.

**Verify:** `go test ./... -run TestCLI` then `go test ./...`.

**Steps:**

- [ ] **Step 1: Add parseSubcommand**

In `main.go`:

```go
// commonFlags is the set of flags every subcommand shares.
type commonFlags struct {
    quiet bool
    help  bool
}

// parseSubcommand registers help + quiet flags, parses args, and
// handles the help/usage exit code logic. The caller passes a
// configure callback to register subcommand-specific flags.
//
// Returns the positional args (caller checks NArg requirements) and
// the parsed common flags.
//
// If parsing failed or help was requested, returns (nil, _, exitCode,
// false) and the caller should return exitCode immediately. Otherwise
// returns (positional, flags, 0, true).
func parseSubcommand(name, helpText string, args []string, stderr io.Writer, configure func(*flag.FlagSet)) ([]string, commonFlags, int, bool) {
    fs := flag.NewFlagSet(name, flag.ContinueOnError)
    fs.SetOutput(stderr)
    quiet := fs.Bool("q", false, "quiet")
    quietLong := fs.Bool("quiet", false, "quiet")
    help := fs.Bool("h", false, "help")
    helpLong := fs.Bool("help", false, "help")
    if configure != nil {
        configure(fs)
    }
    if err := fs.Parse(args); err != nil {
        return nil, commonFlags{}, exitUsage, false
    }
    if *help || *helpLong {
        fmt.Fprint(stderr, helpText)
        return nil, commonFlags{}, exitOK, false
    }
    return fs.Args(), commonFlags{quiet: *quiet || *quietLong}, 0, true
}

// requireOnePositional asserts exactly one positional and prints a
// usage error otherwise.
func requireOnePositional(stderr io.Writer, helpText string, positional []string, label string) bool {
    if len(positional) != 1 {
        fmt.Fprintf(stderr, "expected exactly one %s; got %d\n", label, len(positional))
        fmt.Fprint(stderr, helpText)
        return false
    }
    return true
}
```

- [ ] **Step 2: Rewrite runPack**

```go
func runPack(args []string, stderr io.Writer) int {
    var output, outputLong string
    var keepSource, noVerify, allowCrossFS, force, forceLong bool
    positional, common, exit, ok := parseSubcommand("pack", packHelpText, args, stderr, func(fs *flag.FlagSet) {
        fs.StringVar(&output, "o", "", "output path")
        fs.StringVar(&outputLong, "output", "", "output path")
        fs.BoolVar(&keepSource, "keep-source", false, "keep .scram after verified pack")
        fs.BoolVar(&noVerify, "no-verify", false, "skip round-trip verification")
        fs.BoolVar(&allowCrossFS, "allow-cross-fs", false, "permit auto-delete across filesystems")
        fs.BoolVar(&force, "f", false, "overwrite output")
        fs.BoolVar(&forceLong, "force", false, "overwrite output")
    })
    if !ok {
        return exit
    }
    if !requireOnePositional(stderr, packHelpText, positional, "positional argument (cue path)") {
        return exitUsage
    }
    cuePath := positional[0]
    scramPath := strings.TrimSuffix(cuePath, filepath.Ext(cuePath)) + ".scram"
    out := pickFirst(output, outputLong)
    if out == "" {
        out = DefaultPackOutput(cuePath)
    }
    beForce := force || forceLong
    if !beForce {
        if _, err := os.Stat(out); err == nil {
            fmt.Fprintf(stderr, "output %s already exists (pass -f / --force to overwrite)\n", out)
            return exitUsage
        }
    }
    if noVerify && !keepSource {
        keepSource = true
    }
    rep := NewReporter(stderr, common.quiet)
    if noVerify {
        rep.Info("--no-verify implies --keep-source; original .scram will be kept")
    }
    err := Pack(PackOptions{
        CuePath: cuePath, ScramPath: scramPath, OutputPath: out,
        LeadinLBA: LBALeadinStart, Verify: !noVerify,
    }, rep)
    if err != nil {
        return errToExit(err)
    }
    if !keepSource {
        if removed, removeErr := maybeRemoveSource(scramPath, out, allowCrossFS, rep); removeErr != nil {
            rep.Warn("source removal skipped: %v", removeErr)
        } else if removed {
            rep.Info("removed source %s", scramPath)
        }
    }
    return exitOK
}
```

- [ ] **Step 3: Rewrite runUnpack, runVerify, runInspect on the same pattern**

```go
func runUnpack(args []string, stderr io.Writer) int {
    var output, outputLong string
    var noVerify, force, forceLong bool
    positional, common, exit, ok := parseSubcommand("unpack", unpackHelpText, args, stderr, func(fs *flag.FlagSet) {
        fs.StringVar(&output, "o", "", "output path")
        fs.StringVar(&outputLong, "output", "", "output path")
        fs.BoolVar(&noVerify, "no-verify", false, "skip output hash verification")
        fs.BoolVar(&force, "f", false, "overwrite output")
        fs.BoolVar(&forceLong, "force", false, "overwrite output")
    })
    if !ok {
        return exit
    }
    if !requireOnePositional(stderr, unpackHelpText, positional, "positional argument (container path)") {
        return exitUsage
    }
    containerPath := positional[0]
    out := pickFirst(output, outputLong)
    if out == "" {
        out = DefaultUnpackOutput(containerPath)
    }
    rep := NewReporter(stderr, common.quiet)
    if err := Unpack(UnpackOptions{
        ContainerPath: containerPath, OutputPath: out,
        Verify: !noVerify, Force: force || forceLong,
    }, rep); err != nil {
        return errToExit(err)
    }
    return exitOK
}

func runVerify(args []string, stderr io.Writer) int {
    positional, common, exit, ok := parseSubcommand("verify", verifyHelpText, args, stderr, nil)
    if !ok {
        return exit
    }
    if !requireOnePositional(stderr, verifyHelpText, positional, "positional argument (container path)") {
        return exitUsage
    }
    rep := NewReporter(stderr, common.quiet)
    if err := Verify(VerifyOptions{ContainerPath: positional[0]}, rep); err != nil {
        return errToExit(err)
    }
    return exitOK
}
```

For `runInspect`: it doesn't take `--quiet`, but parseSubcommand registers it harmlessly. To be tidy, leave it — `--quiet` on inspect just becomes a no-op since runInspect doesn't construct a Reporter. The caller's `common.quiet` is simply unused. Acceptable.

```go
func runInspect(args []string, stdout, stderr io.Writer) int {
    var full, asJSON bool
    positional, _, exit, ok := parseSubcommand("inspect", inspectHelpText, args, stderr, func(fs *flag.FlagSet) {
        fs.BoolVar(&full, "full", false, "list every override record")
        fs.BoolVar(&asJSON, "json", false, "machine-readable JSON")
    })
    if !ok {
        return exit
    }
    if !requireOnePositional(stderr, inspectHelpText, positional, "container path") {
        return exitUsage
    }
    path := positional[0]
    m, scramblerHash, delta, err := ReadContainer(path)
    if err != nil {
        fmt.Fprintln(stderr, err)
        return exitIO
    }
    if asJSON {
        body, err := formatJSONInspect(m, delta)
        if err != nil {
            fmt.Fprintln(stderr, err)
            return exitIO
        }
        fmt.Fprintln(stdout, string(body))
        return exitOK
    }
    human, ferr := formatHumanInspect(m, containerMagic, containerVersion, scramblerHash, delta, full)
    fmt.Fprint(stdout, human)
    if ferr != nil {
        fmt.Fprintln(stderr, ferr)
        return exitIO
    }
    return exitOK
}
```

- [ ] **Step 4: Single errToExit**

Replace the three `*ErrorToExit` functions with one:

```go
func errToExit(err error) int {
    if err == nil {
        return exitOK
    }
    var lme *LayoutMismatchError
    switch {
    case errors.As(err, &lme):
        return exitLayout
    case errors.Is(err, errBinHashMismatch):
        return exitWrongBin
    case errors.Is(err, errVerifyMismatch),
         errors.Is(err, errOutputHashMismatch):
        return exitVerifyFail
    default:
        return exitIO
    }
}
```

Delete `packErrorToExit`, `unpackErrorToExit`, `verifyErrorToExit`.

- [ ] **Step 5: Single printHelp**

In `help.go`, replace the five `printXHelp` functions with one:

```go
func printHelp(w io.Writer, text string) {
    fmt.Fprint(w, text)
}
```

Update the `help` case in `run()`:

```go
case "help", "--help", "-h":
    if len(args) >= 2 {
        switch args[1] {
        case "pack":    printHelp(stderr, packHelpText);    return exitOK
        case "unpack":  printHelp(stderr, unpackHelpText);  return exitOK
        case "verify":  printHelp(stderr, verifyHelpText);  return exitOK
        case "inspect": printHelp(stderr, inspectHelpText); return exitOK
        }
    }
    printHelp(stderr, topHelpText)
    return exitOK
```

The non-help dispatch stays unchanged. `printTopHelp(stderr)` callers become `printHelp(stderr, topHelpText)`.

- [ ] **Step 6: Delete filepathDir**

In `main.go`, replace `filepathDir(b)` in `sameFilesystem` with `filepath.Dir(b)`. Delete the `filepathDir` function.

- [ ] **Step 7: Test and commit**

```
go test ./...
git add main.go help.go
git commit -m "cli: parseSubcommand helper, single errToExit, single printHelp"
```

---

## Task 8: Reporter runStep helper

**Goal:** A `runStep` helper that wraps the `Step → fn → Done/Fail` pattern. Refactor pack/unpack/verify to use it.

**Files:**
- Modify: `reporter.go`
- Modify: `pack.go`
- Modify: `unpack.go`
- Modify: `verify.go`

**Acceptance Criteria:**
- [ ] `runStep(r Reporter, label string, fn func() (string, error)) error` exists in `reporter.go`.
- [ ] Pack, Unpack, and Verify use it for every Step/Done/Fail trio that has uniform shape (most do).
- [ ] No behavior change in stderr output.
- [ ] `go test ./...` green.

**Verify:** `go test ./... -run E2E` (any e2e test still produces correct stderr lines via the reporter).

**Steps:**

- [ ] **Step 1: Add runStep**

In `reporter.go`:

```go
// runStep wraps the Step/Done/Fail pattern. fn returns (doneMsg, err);
// on success runStep calls Done(doneMsg), on failure Fail(err) and
// returns the error.
//
// Use for the common case where a step's body is a single computation
// whose result narrates the Done line. Steps with mid-body Info/Warn
// calls or whose Done message depends on multiple values should still
// hand-roll.
func runStep(r Reporter, label string, fn func() (string, error)) error {
    st := r.Step(label)
    msg, err := fn()
    if err != nil {
        st.Fail(err)
        return err
    }
    st.Done("%s", msg)
    return nil
}
```

- [ ] **Step 2: Refactor Pack's steps**

Each `st := r.Step("X"); ...; if err { st.Fail(err); return err }; st.Done(...)` becomes:

```go
if err := runStep(r, "X", func() (string, error) {
    // body
    return "done message", nil
}); err != nil {
    return err
}
```

Apply to: scramble-table self-test, hashing scram, writing container. Skip steps where the function would lift a return value the rest of Pack needs (e.g. ResolveCue, hashing tracks, building ε̂ + delta) — those pull out a value, and the closure return shape doesn't fit. Hand-roll those (or use an extended form, but keep the helper narrow).

Pragmatic guideline: convert a step if the body is ≤4 lines and produces only the done message string. Leave the rest.

- [ ] **Step 3: Refactor Unpack's and Verify's steps similarly**

In Unpack: the scramble-table self-test and "verifying output hashes" steps fit. The bin-hash-verify step now has track-loop logic that doesn't fit; hand-roll it.

In Verify: the "reading manifest" step fits; the "verifying scram hashes" step fits.

- [ ] **Step 4: Test and commit**

```
go test ./...
git add reporter.go pack.go unpack.go verify.go
git commit -m "reporter: runStep helper, applied to uniform Step/Done/Fail cases"
```

---

## Task 9: File mergers — ecma130.go and verify-into-unpack

**Goal:** Merge `ecc.go` + `edc.go` + `scrambler.go` into `ecma130.go`. Fold `verify.go` into `unpack.go`.

**Files:**
- Create: `ecma130.go`
- Create: `ecma130_test.go`
- Delete: `ecc.go`, `edc.go`, `scrambler.go`
- Delete: `ecc_test.go`, `edc_test.go`, `scrambler_test.go`
- Modify: `unpack.go` (absorb `verify.go`'s contents)
- Delete: `verify.go`

**Acceptance Criteria:**
- [ ] `ecma130.go` contains everything from `ecc.go`, `edc.go`, `scrambler.go` with no functional changes.
- [ ] `ecma130_test.go` contains every test from `ecc_test.go`, `edc_test.go`, `scrambler_test.go`.
- [ ] `verify.go` deleted; its `Verify` function and `VerifyOptions` type live in `unpack.go`.
- [ ] No package-level identifier renamed.
- [ ] `go test ./...` green.

**Verify:** `go test ./...`.

**Steps:**

- [ ] **Step 1: Create ecma130.go by concatenation**

```bash
# Build the new file from the three sources.
cat scrambler.go edc.go ecc.go > ecma130.go
```

Then edit `ecma130.go`:
- Keep the file's leading comment as `// /home/hugh/miniscram/ecma130.go\npackage main\n`.
- Merge the three import blocks into one.
- Delete the duplicate `package main` declarations (only the first stays).
- Verify `init()` ordering: scrambler `init()` panics on table mismatch; the EDC `init()` panics similarly; the ECC `init()` panics similarly. Merged file should preserve all three (Go runs them in declaration order within a file, which matches the original cross-file order since lexicographic file ordering had ecc.go before edc.go before scrambler.go — but cross-file init order is not specified by the spec. Inside one file, declaration order is honored. Place scrambler `init()` first, then EDC, then ECC, to match the alphabetical-cross-file ordering observed today.)

Actually: just put the scrambler section first, then EDC, then ECC, with helpful section comments:

```go
// /home/hugh/miniscram/ecma130.go
package main

import (
    "crypto/sha256"
    "encoding/binary"
    "encoding/hex"
    "fmt"
)

// =====================================================================
// ECMA-130 §15: Scrambler (LFSR x^15 + x + 1, seed 0x0001)
// =====================================================================

// ... scrambler.go content (without package/imports)

// =====================================================================
// ECMA-130 §14.3: EDC (CRC-32 over Mode 1 sector prefix)
// =====================================================================

// ... edc.go content

// =====================================================================
// ECMA-130 Annex A: ECC (Reed-Solomon Product Code over GF(2^8))
// =====================================================================

// ... ecc.go content
```

- [ ] **Step 2: Delete the three source files**

```bash
rm scrambler.go edc.go ecc.go
```

- [ ] **Step 3: Concatenate the test files into ecma130_test.go**

Same pattern. Merge imports. Concatenate test functions verbatim. Delete the per-area test files:

```bash
rm scrambler_test.go edc_test.go ecc_test.go
```

- [ ] **Step 4: Compile check**

```
go build ./...
go vet ./...
```

Both must pass before proceeding.

- [ ] **Step 5: Fold verify.go into unpack.go**

Append the contents of `verify.go` (excluding `package main` and the imports) to the end of `unpack.go`. Merge the imports (`"path/filepath"` is already imported by unpack.go; verify.go also uses it). Delete `verify.go`.

- [ ] **Step 6: Test and commit**

```
go test ./...
git add -A
git commit -m "files: merge ecc/edc/scrambler into ecma130, fold verify into unpack"
```

---

## Task 10: Test architecture — fixtures, e2e matrix, CLI sweep

**Goal:** One `fixtures_test.go` with shared helpers. One `e2e_test.go` with the table-driven full-lifecycle matrix. Rename `main_test.go` → `cli_test.go` and extend to cover every subcommand exit-code path. Trim `pack_test.go`/`unpack_test.go`/`verify_test.go`/`inspect_test.go` of redundant content.

**Files:**
- Create: `fixtures_test.go`
- Create: `e2e_test.go`
- Rename: `main_test.go` → `cli_test.go` (and extend)
- Modify: `pack_test.go` (delete tests covered by e2e matrix; keep narrow regression tests like the SafeDisc-class detectWriteOffset bug if not covered elsewhere)
- Modify: `unpack_test.go` (similar)
- Modify: `verify_test.go` (similar)
- Modify: `inspect_test.go` (drop the homemade `buildDelta` and `sampleManifest` — moved to fixtures; drop tests covered by e2e snapshot)
- Modify: `cue_test.go` (only if any duplicate fixture-construction overlaps with fixtures_test.go)
- Modify: `delta_test.go` (only if it borrows from inspect_test's helpers)

**Acceptance Criteria:**
- [ ] `fixtures_test.go` exports `synthDisc(t, opts)`, `writeFixture(t, dir, disc)`, `sampleManifest()`, `mustHashFile(t, path) FileHashes`, `buildDelta(t, offsets) []byte`.
- [ ] `e2e_test.go` runs a table over at least these rows: clean / negative-offset / positive-offset / multi-track-with-audio / mode2 / with-error-sectors. Each row asserts byte-equality of the round-tripped scram against the original.
- [ ] `cli_test.go` covers each subcommand for: success path, missing positional, unknown flag, --help. Includes specific failure paths: bin-hash mismatch on unpack/verify (exit 5), output-hash mismatch on verify (exit 3), container-version mismatch (exit 4).
- [ ] No two test files redundantly construct the same synthetic disc.
- [ ] Total non-redump test LOC ≤ 1700.
- [ ] `go test ./...` green; if `redump_data` fixtures are present locally, `go test -tags redump_data ./...` also green.

**Verify:** `go test ./... -v 2>&1 | tail -50` — visually scan for redundant test names. Then `go test -cover ./...` to check coverage hasn't regressed below ~80% statements.

**Steps:**

- [ ] **Step 1: Create fixtures_test.go**

This file owns the shared synthetic-disc + manifest-construction helpers. The design is:

```go
package main

import (
    "bytes"
    "crypto/md5"
    "crypto/sha1"
    "crypto/sha256"
    "encoding/binary"
    "encoding/hex"
    "io"
    "os"
    "path/filepath"
    "strings"
    "testing"
)

// SynthOpts configures synthDisc. Fields with zero values produce a
// minimal valid disc.
type SynthOpts struct {
    MainSectors    int    // count of "data" sectors before any leadout
    WriteOffset    int    // bytes; positive = scram leads, negative = scram lags
    LeadoutSectors int32  // count of trailing leadout sectors
    Mode           string // cuesheet mode token; default "MODE1/2352"
    ModeByte       byte   // sector byte 15; default 0x01
    Tracks         int    // number of tracks; default 1
    AudioTracks    int    // number of audio tracks (added after data tracks)
    InjectErrors   []int  // 0-based sector indices to corrupt in scram (forces delta records)
}

// SynthDisc is the result of synthDisc: in-memory bin + scram + the
// cuesheet text, ready to write to disk via writeFixture.
type SynthDisc struct {
    Bin     []byte
    Scram   []byte
    Cue     string
    Tracks  []Track // pre-populated tracks (without hashes)
    LeadinLBA int32
    BinFirstLBA int32
    BinSectorCount int32
}

// synthDisc builds an in-memory bin + scram pair satisfying opts.
// Implementation walks the LBA range, generating sectors, scrambling
// the data tracks, and applying writeOffset and inject-errors.
func synthDisc(t *testing.T, opts SynthOpts) SynthDisc {
    t.Helper()
    // ... see below
}

// writeFixture writes disc.Bin, disc.Scram, disc.Cue into dir, naming
// the bin "x.bin", the scram "x.scram", the cue "x.cue". Returns the
// absolute paths.
func writeFixture(t *testing.T, dir string, disc SynthDisc) (binPath, scramPath, cuePath string) {
    t.Helper()
    binPath = filepath.Join(dir, "x.bin")
    scramPath = filepath.Join(dir, "x.scram")
    cuePath = filepath.Join(dir, "x.cue")
    if err := os.WriteFile(binPath, disc.Bin, 0o644); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(scramPath, disc.Scram, 0o644); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(cuePath, []byte(disc.Cue), 0o644); err != nil {
        t.Fatal(err)
    }
    return
}

// sampleManifest returns a Manifest with realistic-looking but
// deterministic values for inspect/format tests. Hashes are
// repetition strings (not real hashes of any file).
func sampleManifest() *Manifest {
    return &Manifest{
        ToolVersion: "miniscram 1.0.0 (go1.22)",
        CreatedUTC:  "2026-04-28T14:30:21Z",
        WriteOffsetBytes: -52,
        LeadinLBA:        -150,
        Scram: ScramInfo{
            Size: 739729728,
            Hashes: FileHashes{
                MD5:    strings.Repeat("1", 32),
                SHA1:   strings.Repeat("2", 40),
                SHA256: strings.Repeat("c", 64),
            },
        },
        Tracks: []Track{{
            Number:   1, Mode: "MODE1/2352", FirstLBA: 0,
            Filename: "x.bin", Size: 235200,
            Hashes: FileHashes{
                MD5:    strings.Repeat("a", 32),
                SHA1:   strings.Repeat("b", 40),
                SHA256: strings.Repeat("d", 64),
            },
        }},
    }
}

// mustHashFile streams path through MD5+SHA-1+SHA-256 and returns all
// three hex-encoded. Replaces the sha256-only mustHashFile in v0.2.
func mustHashFile(t *testing.T, path string) FileHashes {
    t.Helper()
    f, err := os.Open(path)
    if err != nil { t.Fatal(err) }
    defer f.Close()
    m, s1, s256 := md5.New(), sha1.New(), sha256.New()
    if _, err := io.Copy(io.MultiWriter(m, s1, s256), f); err != nil {
        t.Fatal(err)
    }
    return FileHashes{
        MD5:    hex.EncodeToString(m.Sum(nil)),
        SHA1:   hex.EncodeToString(s1.Sum(nil)),
        SHA256: hex.EncodeToString(s256.Sum(nil)),
    }
}

// buildDelta returns a delta payload with one 1-byte override per
// offset in offs. Used by inspect/delta tests that need a small
// well-formed delta without spinning up a builder.
func buildDelta(t *testing.T, offs []uint64) []byte {
    t.Helper()
    var buf bytes.Buffer
    var hdr [4]byte
    binary.BigEndian.PutUint32(hdr[:], uint32(len(offs)))
    buf.Write(hdr[:])
    for _, off := range offs {
        var rec [12]byte
        binary.BigEndian.PutUint64(rec[:8], off)
        binary.BigEndian.PutUint32(rec[8:], 1)
        buf.Write(rec[:])
        buf.WriteByte(0xFF)
    }
    return buf.Bytes()
}
```

For the `synthDisc` body: take the existing `synthDiscWithMode` from `pack_test.go` (or wherever it lives) as the starting point. Generalize:
- Multi-track support: emit one FILE per track in the cuesheet, sized at `MainSectors * SectorSize / Tracks` bytes per track (rounded down). Set per-track FirstLBA correctly (cumulative file size).
- AudioTracks: emit additional FILE entries after the data tracks. Audio bin content is random-but-deterministic bytes; scram content is identical (audio isn't scrambled).
- InjectErrors: after building the scram, flip a byte in the corresponding LBA's scram data so the delta has to record an override.

Reference the current `synthDisc` in `pack_test.go` (look for "func synthDisc" or "func synthDiscWithMode") for the LBA-walk shape.

- [ ] **Step 2: Create e2e_test.go**

```go
package main

import (
    "bytes"
    "io"
    "os"
    "path/filepath"
    "testing"
)

type e2eRow struct {
    name string
    opts SynthOpts
}

func TestE2EMatrix(t *testing.T) {
    rows := []e2eRow{
        {"clean",            SynthOpts{MainSectors: 100, LeadoutSectors: 10}},
        {"negative-offset",  SynthOpts{MainSectors: 100, LeadoutSectors: 10, WriteOffset: -48}},
        {"positive-offset",  SynthOpts{MainSectors: 100, LeadoutSectors: 10, WriteOffset: 48}},
        {"mode2",            SynthOpts{MainSectors: 100, LeadoutSectors: 10, Mode: "MODE2/2352", ModeByte: 0x02}},
        {"with-errors",      SynthOpts{MainSectors: 100, LeadoutSectors: 10, InjectErrors: []int{12, 47, 63}}},
        {"data-plus-audio",  SynthOpts{MainSectors: 100, LeadoutSectors: 10, AudioTracks: 1}},
    }
    for _, row := range rows {
        t.Run(row.name, func(t *testing.T) {
            dir := t.TempDir()
            disc := synthDisc(t, row.opts)
            _, scramPath, cuePath := writeFixture(t, dir, disc)
            outPath := filepath.Join(dir, "x.miniscram")

            // Pack.
            rep := NewReporter(io.Discard, true)
            if err := Pack(PackOptions{
                CuePath: cuePath, ScramPath: scramPath, OutputPath: outPath,
                LeadinLBA: LBAPregapStart, Verify: true,
            }, rep); err != nil {
                t.Fatalf("Pack: %v", err)
            }

            // Inspect (smoke).
            m, _, _, err := ReadContainer(outPath)
            if err != nil {
                t.Fatalf("ReadContainer: %v", err)
            }
            if m.WriteOffsetBytes != row.opts.WriteOffset {
                t.Fatalf("write offset: got %d want %d", m.WriteOffsetBytes, row.opts.WriteOffset)
            }

            // Verify.
            if err := Verify(VerifyOptions{ContainerPath: outPath}, rep); err != nil {
                t.Fatalf("Verify: %v", err)
            }

            // Pack consumed the .scram (no --keep-source); rewrite it.
            if err := os.WriteFile(scramPath, disc.Scram, 0o644); err != nil {
                t.Fatal(err)
            }

            // Unpack into a fresh path.
            recovered := filepath.Join(dir, "x.recovered.scram")
            if err := Unpack(UnpackOptions{
                ContainerPath: outPath, OutputPath: recovered,
                Verify: true, Force: true,
            }, rep); err != nil {
                t.Fatalf("Unpack: %v", err)
            }
            recoveredBytes, err := os.ReadFile(recovered)
            if err != nil { t.Fatal(err) }
            if !bytes.Equal(recoveredBytes, disc.Scram) {
                t.Fatalf("byte mismatch: recovered scram differs from original")
            }
        })
    }
}
```

(Note: in v0.2, runPack — not Pack — handles the `--keep-source` semantics. Pack itself does not delete the .scram. So the "rewrite scram" step above is actually unnecessary; remove it. Keeping it documented here as a reminder during implementation.)

Actually since we're testing `Pack` (the library function, not `runPack`), Pack does not delete the scram. Remove the `os.WriteFile` step.

- [ ] **Step 3: Rename main_test.go → cli_test.go and extend**

```bash
git mv main_test.go cli_test.go
```

Add tests for each subcommand's success path, --help, and the failure paths from the acceptance criteria. Example:

```go
func TestCLIPackHappyPath(t *testing.T) {
    dir := t.TempDir()
    disc := synthDisc(t, SynthOpts{MainSectors: 50, LeadoutSectors: 5})
    _, _, cuePath := writeFixture(t, dir, disc)
    var stderr bytes.Buffer
    code := run([]string{"pack", "-q", cuePath}, io.Discard, &stderr)
    if code != exitOK {
        t.Fatalf("got %d, stderr=%s", code, stderr.String())
    }
    if _, err := os.Stat(filepath.Join(dir, "x.miniscram")); err != nil {
        t.Fatalf("output not created: %v", err)
    }
}

func TestCLIVerifyWrongBin(t *testing.T) {
    dir := t.TempDir()
    disc := synthDisc(t, SynthOpts{MainSectors: 50, LeadoutSectors: 5})
    _, _, cuePath := writeFixture(t, dir, disc)
    var stderr bytes.Buffer
    code := run([]string{"pack", "-q", cuePath}, io.Discard, &stderr)
    if code != exitOK { t.Fatalf("pack failed: %d", code) }

    // Corrupt the bin so verify's bin-hash check fails.
    binPath := filepath.Join(dir, "x.bin")
    bin, _ := os.ReadFile(binPath)
    bin[100] ^= 0xFF
    os.WriteFile(binPath, bin, 0o644)

    code = run([]string{"verify", "-q", filepath.Join(dir, "x.miniscram")}, io.Discard, &stderr)
    if code != exitWrongBin {
        t.Fatalf("got %d want %d, stderr=%s", code, exitWrongBin, stderr.String())
    }
}

func TestCLIInspectVersionMismatch(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "fake.miniscram")
    body := []byte("MSCM\xFF") // version byte 0xFF
    body = append(body, make([]byte, containerHeaderSize-5)...)
    os.WriteFile(path, body, 0o644)
    var stderr bytes.Buffer
    code := run([]string{"inspect", path}, io.Discard, &stderr)
    if code != exitIO {
        t.Fatalf("got %d want %d", code, exitIO)
    }
    if !bytes.Contains(stderr.Bytes(), []byte("unsupported container version")) {
        t.Fatalf("missing version-mismatch message: %s", stderr.String())
    }
}
```

Plus tests for each subcommand's `--help` exit and unknown-flag exit. Aim for about 12-15 CLI tests total.

- [ ] **Step 4: Trim pack_test.go / unpack_test.go / verify_test.go**

Walk each file. Delete tests whose behavior is already covered by an `e2eRow`. Keep:
- Specific narrow regression tests (if any) — e.g. a test that pins the SafeDisc-class detectWriteOffset bug fix.
- Tests of error paths not covered by the e2e matrix — e.g. "pack rejects scram with mismatched bin size".
- The `TestPack*` / `TestUnpack*` / `TestVerify*` that exercise the *library* function directly with edge-case inputs.

Move shared helpers (`synthDiscWithMode`, `writeSynthDiscFiles`, etc.) into `fixtures_test.go` or delete if `synthDisc(opts)` covers them.

Delete `var _ = bytes.Equal` from pack_test.go.

After this step, total LOC across pack_test+unpack_test+verify_test should be ≤ 600 (was ~1100).

- [ ] **Step 5: Trim inspect_test.go**

Delete the local `sampleManifest` and `buildDelta` (they're in fixtures_test.go now). Delete tests whose only assertion is "pack a synth disc and check the inspect output mentions field X" — that's covered by `TestE2EMatrix` via inspect-smoke + the format-output snapshots that remain.

Keep:
- `formatHumanInspect` snapshot test on the full sampleManifest.
- `formatJSONInspect` round-trip test.
- `--full` override-listing test.
- Format edge cases (empty delta, lots of records, etc.).

After: ~150 LOC (was 480).

- [ ] **Step 6: Verify total test LOC**

```bash
wc -l *_test.go | sort -rn
```

Total non-redump test LOC should be ≤ 1700.

- [ ] **Step 7: Run full suite + commit**

```
go test ./...
go test -cover ./...
git add -A
git commit -m "tests: shared fixtures, e2e matrix, CLI exit-code coverage"
```

---

## Task 11: README + cleanup pass

**Goal:** Top-level `README.md` documenting the v1 format. Tool version string already at `miniscram 1.0.0` from Task 1; finish any miscellaneous cleanup.

**Files:**
- Create: `README.md`
- Modify: `pack.go` (any remaining stale comment fixups)
- Modify: any file with leftover dead code identified during the refactor

**Acceptance Criteria:**
- [ ] `README.md` documents:
  1. What miniscram is (1-paragraph elevator pitch).
  2. Each CLI subcommand (pack/unpack/verify/inspect) with one example invocation each.
  3. Exit codes table.
  4. Container format v1: byte-level binary header layout, JSON manifest schema, delta payload wire format. A reader implementing only this spec must be able to read a miniscram container.
  5. Pointer to `docs/superpowers/specs/` for design rationale.
- [ ] No dead code: `go vet ./...` clean.
- [ ] No stale comments referencing removed functions or v0.x semantics. Spot-check `pack.go`, `unpack.go`, `manifest.go`.
- [ ] `go test ./...` green.

**Verify:** `cat README.md` and read it as a stranger; can you implement a reader from this alone?

**Steps:**

- [ ] **Step 1: Write README.md**

Use the spec's "Container format v1" section as the source for the format documentation. Structure:

```markdown
# miniscram

Compactly preserve scrambled CD-ROM dumps. Stores the bytes of a
Redumper `.scram` file as a small structured delta against the
unscrambled `.bin`. With miniscram and your `.bin`, you can reproduce
the original `.scram` byte-for-byte. Implements the method from
Hauenstein, "Compact Preservation of Scrambled CD-ROM Data"
(IJCSIT, August 2022), specialised for Redumper output.

## Install

```sh
go install ./...    # produces ./miniscram
```

## CLI

### pack

Pack a `.scram` into a `.miniscram` container.

    miniscram pack disc.cue [-o out.miniscram] [-f] [--no-verify] [--keep-source]

Reads `disc.scram` (derived from the cue stem) and the `.bin` files
referenced by `disc.cue`. Writes `disc.miniscram` (or `-o`) and
removes `disc.scram` after a verified round-trip.

### unpack

Reproduce the `.scram` from `.bin` + `.miniscram`.

    miniscram unpack disc.miniscram [-o out.scram] [-f] [--no-verify]

### verify

Non-destructive integrity check.

    miniscram verify disc.miniscram

Rebuilds the recovered `.scram` in a temp file, hashes it, compares
against the manifest's recorded hashes, deletes the temp file.

### inspect

Pretty-print a container.

    miniscram inspect disc.miniscram [--full] [--json]

## Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | usage / input error |
| 2 | layout mismatch |
| 3 | verification failed |
| 4 | I/O error |
| 5 | wrong .bin for this .miniscram |

## Container format (v1)

### File structure

A `.miniscram` file is laid out as:

    binary header   41 bytes (fixed)
    manifest body   variable (JSON)
    delta payload   variable (binary)

### Binary header (41 bytes)

| Byte range | Field | Type | Notes |
|---|---|---|---|
| `[0, 4)`   | `magic` | 4 bytes | ASCII `"MSCM"` |
| `[4, 5)`   | `version` | 1 byte | `0x01` for v1 |
| `[5, 37)`  | `scrambler_table_sha256` | 32 bytes | Raw SHA-256 of the ECMA-130 scramble table |
| `[37, 41)` | `manifest_length` | u32 BE | Byte count of the manifest body |

A reader rejects the container if the magic is wrong, the version
isn't `0x01`, or `scrambler_table_sha256` doesn't match the reader's
own ECMA-130 implementation.

### Manifest (JSON)

UTF-8 encoded JSON object. All fields are required.

```json
{
  "tool_version": "miniscram 1.0.0 (go1.22)",
  "created_utc": "2026-04-28T14:30:21Z",
  "write_offset_bytes": -52,
  "leadin_lba": -45150,
  "scram": {
    "size": 739729728,
    "hashes": {"md5": "...", "sha1": "...", "sha256": "..."}
  },
  "tracks": [
    {
      "number": 1,
      "mode": "MODE1/2352",
      "first_lba": 0,
      "filename": "DeusEx_v1002f.bin",
      "size": 739729728,
      "hashes": {"md5": "...", "sha1": "...", "sha256": "..."}
    }
  ]
}
```

Field semantics:

- `write_offset_bytes`: signed bytes; positive = scram leads, negative = scram lags. Always a multiple of 4 (audio sample alignment).
- `leadin_lba`: integer LBA where the scram file begins on disc. Real Redumper output uses `-45150`; truncated test fixtures may use higher values.
- `scram.size`: byte length of the original `.scram`.
- `scram.hashes`: lowercase hex MD5, SHA-1, SHA-256 of the original `.scram`.
- `tracks[*].number`: 1-indexed track number per the cuesheet.
- `tracks[*].mode`: one of `"MODE1/2352"`, `"MODE2/2352"`, `"AUDIO"`.
- `tracks[*].first_lba`: absolute LBA where the track's `.bin` begins on disc (cumulative file size in sectors).
- `tracks[*].filename`: basename of the track's `.bin`. No paths.
- `tracks[*].size`: byte length of the track's `.bin`. Always a multiple of `2352`.
- `tracks[*].hashes`: lowercase hex MD5, SHA-1, SHA-256 of the track's `.bin`.

### Delta payload (binary)

Big-endian. Begins immediately after the manifest body.

| Field | Type | Notes |
|---|---|---|
| `count` | u32 | Number of override records |
| `record[i]` | variable | See below |

Each `record[i]`:

| Field | Type | Notes |
|---|---|---|
| `file_offset` | u64 | Byte offset within the recovered `.scram` |
| `length` | u32 | Payload length, `1 ≤ length ≤ scram.size` |
| `payload` | `length` bytes | Bytes to write at `file_offset` |

To reconstruct the `.scram`, a reader:
1. Reads bin files in cue order, scrambling all non-AUDIO tracks via ECMA-130 §15.
2. Synthesises leadin (zeros), pregap (Mode 1 zero sectors), and leadout (Mode 0 zero sectors) regions per ECMA-130 §14.
3. Concatenates everything into a buffer matching `scram.size`.
4. Applies each delta record by overwriting `length` bytes starting at `file_offset`.

The result must hash to `scram.hashes`.

## Design history

Architecture, design rationale, and per-feature decisions live in
`docs/superpowers/specs/`. This README is the authoritative reference
for the wire format only.
```

- [ ] **Step 2: Final cleanup sweep**

Search for any remaining stale references:

```bash
grep -nE "v0\.2|miniscram 0\.|format_version|bin_md5|bin_sha|error_sectors|delta_size|scrambler_table_sha256" *.go
```

Anything that turns up should either be intentionally there (e.g. a backwards-compatible code path that doesn't exist post-v1) or removed.

```bash
grep -nE "BuildEpsilonHatAndDelta|verifyRoundTrip|filepathDir|deltaJSONSize|ScramOffset" *.go
```

These are functions removed during the refactor; if any reference remains, it's stale.

```bash
grep -n "TODO\|FIXME\|XXX" *.go
```

Triage any matches. Keep ones that document genuine future work; delete or address the rest.

- [ ] **Step 3: Verify final shape**

```bash
go vet ./...
go test ./...
go test -cover ./...
wc -l *.go | sort -rn
```

Acceptance:
- Source LOC (non-test): ≤ 2300.
- Test LOC: ≤ 1700.
- Coverage: not lower than `main` baseline (run `go test -cover ./...` on `main` first to capture baseline if uncertain).

- [ ] **Step 4: Commit**

```
git add -A
git commit -m "v1: README documenting wire format, final cleanup"
```

---

## Self-review notes

This plan covers every section of the spec:
- Format v1 (binary header + slim manifest + delta lift): Task 1 + Task 4.
- Component layout: distributed across Tasks 2, 3, 4, 5, 6, 7, 8, 9.
- Test architecture: Task 10.
- Migration phases: Tasks 1-11 in order.
- Acceptance criteria: each task lists the spec criteria it satisfies.

Things to watch out for during execution:
- **Task 1 is the gating change.** If it leaves something half-broken, every subsequent task suffers. Don't move on until `go test ./...` is green.
- **Task 9's file mergers are mechanical but easy to typo.** A misplaced `init()` or duplicate `package` declaration will break the build noisily — that's fine, fix and continue.
- **Task 10's test sweep is the easiest place to delete coverage by accident.** When deleting a test, confirm an e2e row covers the same path; if not, add the row first.
- **e2e_redump_test.go uses build tags (`redump_data`).** It only runs when fixtures are present. Compilation must still succeed without the tag set; verify with `go build ./...` after tasks that touch builder/pack/unpack/verify.
