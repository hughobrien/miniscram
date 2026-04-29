# Compressed Delta Payload Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Shrink `.miniscram` files ~16:1 by zlib-compressing the delta payload at the container boundary.

**Architecture:** `WriteContainer` wraps the post-manifest file writer in `zlib.NewWriterLevel(f, zlib.BestCompression)` and copies the delta source through it. `ReadContainer` wraps the post-manifest file reader in `zlib.NewReader` and `io.ReadAll`s the plaintext delta out of it. Everything downstream of `ReadContainer` continues to consume a plaintext `[]byte`, so `DeltaEncoder`, `ApplyDelta`, `IterateDeltaRecords`, `Inspect`, and `Verify` are unchanged. Container version byte stays `0x01` — only one v1 file exists in the world (`/tmp/HALFLIFE.miniscram`) and it can be regenerated.

**Tech Stack:** Go stdlib `compress/zlib`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-04-28-miniscram-delta-zlib-design.md`

---

## File Structure

| File | Change | Responsibility |
|---|---|---|
| `manifest.go` | modify | Add zlib wrap in `WriteContainer` and `ReadContainer` |
| `manifest_test.go` | modify | Add two new sub-tests: zlib magic on disk, plaintext-v1 rejection |
| `README.md` | modify | One-line note in the Delta payload section |

No new files. No package-level helpers extracted (the call sites are one each).

---

### Task 1: zlib-compress delta payload at container boundary

**Goal:** `WriteContainer` emits a zlib stream after the manifest; `ReadContainer` decompresses it; tests confirm round-trip and plaintext-v1 rejection.

**Files:**
- Modify: `manifest.go:75-125` (`WriteContainer`) and `manifest.go:129-173` (`ReadContainer`)
- Modify: `manifest_test.go` (add two new test funcs)

**Acceptance Criteria:**
- [ ] `WriteContainer` uses `zlib.NewWriterLevel(f, zlib.BestCompression)` for delta bytes; closes the writer before file `Sync` / `Close`
- [ ] `ReadContainer` uses `zlib.NewReader` on the post-manifest tail; wraps decompression errors as `decompressing delta payload: %w`
- [ ] `TestContainerRoundtrip` continues to pass unmodified (zlib wrap is symmetric)
- [ ] New test `TestContainerDeltaIsZlibFramed` asserts the on-disk bytes immediately after the manifest start with `0x78` (zlib magic) and that the encoded delta is smaller than the plaintext input
- [ ] New test `TestContainerRejectsPlaintextDelta` constructs a `.miniscram` whose post-manifest bytes are plaintext, asserts `ReadContainer` returns an error whose message contains `decompressing delta payload`
- [ ] `go vet ./...` clean
- [ ] `go test -count=1 ./...` passes
- [ ] `gofmt -l .` empty
- [ ] `just test-sanitizers` passes (race + msan + asan)

**Verify:** `just check && just test-sanitizers` → vet, fmt-check, full test suite, and all three sanitizers all pass.

**Steps:**

- [ ] **Step 1: Write the failing tests**

Open `manifest_test.go` and append two new test funcs after `TestContainerRejectsInvalid`. Add `"encoding/binary"`, `"encoding/hex"`, and `"strings"` to the imports if not already present.

```go
func TestContainerDeltaIsZlibFramed(t *testing.T) {
	m := &Manifest{
		ToolVersion: "miniscram-test",
		CreatedUTC:  "2026-04-28T00:00:00Z",
		Scram:       ScramInfo{Size: 0, Hashes: FileHashes{MD5: "0", SHA1: "0", SHA256: "0"}},
		Tracks: []Track{{
			Number: 1, Mode: "MODE1/2352", FirstLBA: 0, Size: 0, Filename: "t.bin",
			Hashes: FileHashes{MD5: "0", SHA1: "0", SHA256: "0"},
		}},
	}
	delta := bytes.Repeat([]byte("ABCDEFGH"), 1024)
	path := filepath.Join(t.TempDir(), "x.miniscram")
	if err := WriteContainer(path, m, bytes.NewReader(delta)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mlen := binary.BigEndian.Uint32(raw[37:41])
	postManifest := raw[41+int(mlen):]
	if len(postManifest) < 2 {
		t.Fatalf("post-manifest too short: %d bytes", len(postManifest))
	}
	if postManifest[0] != 0x78 {
		t.Fatalf("expected zlib magic 0x78 at start of post-manifest, got 0x%02x", postManifest[0])
	}
	if len(postManifest) >= len(delta) {
		t.Fatalf("compression no-op: post-manifest %d bytes vs plaintext %d bytes", len(postManifest), len(delta))
	}
}

func TestContainerRejectsPlaintextDelta(t *testing.T) {
	tableHash, err := hex.DecodeString(expectedScrambleTableSHA256)
	if err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"tool_version":"x","created_utc":"x","write_offset_bytes":0,"leadin_lba":0,` +
		`"scram":{"size":0,"hashes":{"md5":"0","sha1":"0","sha256":"0"}},` +
		`"tracks":[{"number":1,"mode":"MODE1/2352","first_lba":0,"filename":"t.bin","size":0,` +
		`"hashes":{"md5":"0","sha1":"0","sha256":"0"}}]}`)
	var buf bytes.Buffer
	buf.WriteString(containerMagic)
	buf.WriteByte(containerVersion)
	buf.Write(tableHash)
	binary.Write(&buf, binary.BigEndian, uint32(len(manifest)))
	buf.Write(manifest)
	buf.Write([]byte{0, 0, 0, 0}) // plaintext count = 0
	path := filepath.Join(t.TempDir(), "plain.miniscram")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = ReadContainer(path)
	if err == nil {
		t.Fatalf("expected error reading plaintext-delta v1 file")
	}
	if !strings.Contains(err.Error(), "decompressing delta payload") {
		t.Fatalf("expected error to mention 'decompressing delta payload', got: %v", err)
	}
}
```

- [ ] **Step 2: Run tests; verify they fail**

```
go test -count=1 -run 'TestContainerDeltaIsZlibFramed|TestContainerRejectsPlaintextDelta' -v
```

Expected: `TestContainerDeltaIsZlibFramed` fails because post-manifest starts with `0x41` (`'A'` from the repeated payload) not `0x78`. `TestContainerRejectsPlaintextDelta` fails because `ReadContainer` returns a `nil` error on the plaintext file.

- [ ] **Step 3: Add zlib import and wrap WriteContainer**

In `manifest.go`, add `"compress/zlib"` to the import block. Replace the `io.Copy(f, deltaSrc)` block in `WriteContainer` (currently around line 114):

```go
	if _, err := f.Write(body); err != nil {
		return err
	}
	zw, err := zlib.NewWriterLevel(f, zlib.BestCompression)
	if err != nil {
		return fmt.Errorf("creating zlib writer: %w", err)
	}
	if _, err := io.Copy(zw, deltaSrc); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("flushing zlib writer: %w", err)
	}
	if err := f.Sync(); err != nil {
		return err
	}
```

- [ ] **Step 4: Wrap ReadContainer**

Replace the `io.ReadAll(f)` block in `ReadContainer` (currently around line 168):

```go
	zr, err := zlib.NewReader(f)
	if err != nil {
		return nil, [32]byte{}, nil, fmt.Errorf("decompressing delta payload: %w", err)
	}
	defer zr.Close()
	delta, err := io.ReadAll(zr)
	if err != nil {
		return nil, [32]byte{}, nil, fmt.Errorf("decompressing delta payload: %w", err)
	}
	return &m, scramblerHash, delta, nil
```

- [ ] **Step 5: Run the new tests; verify they pass**

```
go test -count=1 -run 'TestContainerDeltaIsZlibFramed|TestContainerRejectsPlaintextDelta' -v
```

Expected: both pass.

- [ ] **Step 6: Run full check and sanitizers**

```
just check && just test-sanitizers
```

Expected: vet, fmt-check, full test suite, race, msan, and asan all clean.

- [ ] **Step 7: Commit**

```
git add manifest.go manifest_test.go
git commit -m "feat: zlib-compress delta payload (16:1 win on Half-Life)"
```

---

### Task 2: README delta-payload note + measured size verification

**Goal:** Document the wire-format change in the README and confirm the size win on the Half-Life real-disc fixture.

**Files:**
- Modify: `README.md` (the "Delta payload (binary)" section, lines ~123-138)

**Acceptance Criteria:**
- [ ] README states that the bytes after the manifest are a `compress/zlib` `BestCompression` stream of the delta layout
- [ ] Re-packing `half-life/HALFLIFE.cue` produces a `.miniscram` ≤ 1 MB (pre-change size was 5,491,722 bytes; expected post-change ≈ 350 KB)
- [ ] The new file round-trips through `verify` cleanly

**Verify:**
```
go build -o /tmp/miniscram-zlib . && \
  /tmp/miniscram-zlib pack -o /tmp/HALFLIFE-zlib.miniscram --keep-source -f -q half-life/HALFLIFE.cue && \
  ls -la /tmp/HALFLIFE-zlib.miniscram && \
  /tmp/miniscram-zlib verify /tmp/HALFLIFE-zlib.miniscram
```
→ size ≤ 1 MB; verify exits 0.

**Steps:**

- [ ] **Step 1: Update the README**

In `README.md`, find the "### Delta payload (binary)" heading. Replace its first line:

```
Big-endian. Begins immediately after the manifest body.
```

with:

```
Begins immediately after the manifest body, as a `compress/zlib`
`BestCompression` stream. Decompressed, the layout is the big-endian
record sequence below.
```

Leave the table and reconstruction steps unchanged.

- [ ] **Step 2: Build and pack Half-Life**

```
go build -o /tmp/miniscram-zlib .
/tmp/miniscram-zlib pack -o /tmp/HALFLIFE-zlib.miniscram --keep-source -f -q half-life/HALFLIFE.cue
ls -la /tmp/HALFLIFE-zlib.miniscram
```

Expected: file size ≤ 1 MB (target ≈ 350 KB).

- [ ] **Step 3: Round-trip verify the new file**

```
/tmp/miniscram-zlib verify /tmp/HALFLIFE-zlib.miniscram
```

Expected: exits 0.

- [ ] **Step 4: Confirm any pre-change v1 file is rejected with a clear error**

```
[ -f /tmp/HALFLIFE.miniscram ] && /tmp/miniscram-zlib verify /tmp/HALFLIFE.miniscram 2>&1 | head -3 || echo "no pre-change file present, skipping"
```

Expected (if file present): error mentioning `decompressing delta payload`. If absent, skip.

- [ ] **Step 5: Commit**

```
git add README.md
git commit -m "docs: README delta-payload section notes zlib framing"
```

---

## Self-Review

**Spec coverage:**
- Wire format change → Task 1 Steps 3 (write) + 4 (read) ✓
- Three call sites (WriteContainer, ReadContainer, README) → Task 1 + Task 2 ✓
- Round-trip + zlib-magic test → Task 1 Step 1 (`TestContainerDeltaIsZlibFramed`) ✓
- Plaintext-rejection test → Task 1 Step 1 (`TestContainerRejectsPlaintextDelta`) ✓
- Existing e2e suite unchanged → Task 1 Step 6 ✓
- Error wrapping `decompressing delta payload: %w` → Task 1 Step 4 ✓
- Size win confirmation → Task 2 Step 2 ✓

**Placeholder scan:** none — every step has the actual code or command.

**Type consistency:** `containerMagic`, `containerVersion`, `expectedScrambleTableSHA256`, `WriteContainer`, `ReadContainer`, `Manifest`, `ScramInfo`, `Track`, `FileHashes` are all real identifiers in the existing codebase. New test func names don't collide with `TestContainerRoundtrip` or `TestContainerRejectsInvalid`.
