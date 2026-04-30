# miniscram container format v2 — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace miniscram's v1 container layout (fixed header + length-prefixed JSON manifest + zlib delta) with a PNG/CHD-style chunk format: 5-byte file header followed by `MFST`, `TRKS`, `HASH`, `DLTA` chunks, each with per-chunk CRC32.

**Architecture:** Hand-rolled chunk reader/writer using only Go stdlib (`encoding/binary`, `hash/crc32`). The Manifest in-memory struct stays the same shape; what changes is its on-disk encoding. Each chunk type gets a payload codec that operates on a `[]byte`. `WriteContainer`/`ReadContainer` orchestrate header + chunk emission/walking and validate the four critical chunks are present, unique, and ordered (MFST first).

**Tech Stack:** Go 1.x, stdlib only. CRC-32/IEEE polynomial via `crc32.IEEETable` (matches PNG). 16 MiB length sanity cap on every non-`DLTA` chunk.

**Spec:** `docs/superpowers/specs/2026-04-29-miniscram-container-format-v2-design.md`

---

## File structure

| File | Status | Responsibility |
|---|---|---|
| `chunks.go` | new | Chunk-framing primitives (`writeChunk`, `readChunk`, CRC, fourcc) and per-chunk-type payload codecs (`encode/decode` for MFST/TRKS/HASH) |
| `chunks_test.go` | new | Tests for primitives + codec round-trips + corruption rejection |
| `manifest.go` | modify | Replace `WriteContainer`/`ReadContainer` bodies; migrate `CreatedUTC string` → `CreatedUnix int64`; drop JSON marshaling path |
| `manifest_test.go` | modify | Adapt round-trip and existing rejection tests for v2 |
| `pack.go` | modify | Use `time.Now().UTC().Unix()`; drop `runtime.Version()` suffix from `tool_version` |
| `inspect.go` | modify | Format `CreatedUnix` int64 back to RFC3339 for human display; JSON-mode output retains its current shape |
| `inspect_test.go` | modify | Update assertions for the renamed field and reformatted display |
| `e2e_redump_test.go` | modify | Refresh per-fixture container-size bounds after the format change |

`unpack.go`, `verify_test.go`, `unpack_test.go`, `e2e_test.go` are already adapted for the slim-header v2 commit on this branch and need no further changes apart from incidental renames.

---

## Task 1: chunk-framing primitives

**Goal:** Implement `writeChunk`/`readChunk` with CRC32 over `(type || payload)` and a 16 MiB length sanity cap on every non-`DLTA` chunk.

**Files:**
- Create: `chunks.go`
- Create: `chunks_test.go`

**Acceptance Criteria:**
- [ ] `writeChunk(w, tag, payload)` emits `tag(4) + length(4 BE) + payload + CRC32(4 BE)`
- [ ] `readChunk(r)` returns `(tag, payload, err)`; verifies CRC; enforces 16 MiB cap on non-`DLTA` tags; returns `io.EOF` on clean EOF before any byte read; wraps `io.ErrUnexpectedEOF` on mid-chunk truncation
- [ ] Round-trip test: arbitrary tag + payload survives `writeChunk`→`readChunk` byte-equal
- [ ] Bad-CRC test: flipping any byte of the 4-byte CRC trailer produces a clear `"chunk %q crc mismatch"` error
- [ ] Length-cap test: synthesizing a non-`DLTA` chunk header with `length = 16<<20 + 1` is rejected before any payload bytes are read; `DLTA` with the same length proceeds (and then EOFs since the test buffer has no payload)

**Verify:** `go test -run TestChunk -v ./...` → all chunk-primitive tests pass

**Steps:**

- [ ] **Step 1: Write the failing tests in `chunks_test.go`**

```go
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestChunkRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	tag := fourcc("TEST")
	payload := []byte("hello world")
	if err := writeChunk(&buf, tag, payload); err != nil {
		t.Fatal(err)
	}
	gotTag, gotPayload, err := readChunk(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if gotTag != tag {
		t.Errorf("tag: got %v, want %v", gotTag, tag)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Errorf("payload: got %q, want %q", gotPayload, payload)
	}
}

func TestChunkEmptyPayload(t *testing.T) {
	var buf bytes.Buffer
	if err := writeChunk(&buf, fourcc("EMPT"), nil); err != nil {
		t.Fatal(err)
	}
	tag, payload, err := readChunk(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if tag != fourcc("EMPT") || len(payload) != 0 {
		t.Fatalf("got tag=%v len=%d", tag, len(payload))
	}
}

func TestChunkCleanEOF(t *testing.T) {
	_, _, err := readChunk(bytes.NewReader(nil))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF on empty reader, got %v", err)
	}
}

func TestChunkTruncatedHeader(t *testing.T) {
	_, _, err := readChunk(bytes.NewReader([]byte{'M', 'F'}))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected io.ErrUnexpectedEOF, got %v", err)
	}
}

func TestChunkTruncatedPayload(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{'M', 'F', 'S', 'T'})
	binary.Write(&buf, binary.BigEndian, uint32(100))
	buf.Write(make([]byte, 50)) // only half the payload
	_, _, err := readChunk(&buf)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected io.ErrUnexpectedEOF, got %v", err)
	}
}

func TestChunkBadCRC(t *testing.T) {
	var buf bytes.Buffer
	if err := writeChunk(&buf, fourcc("TEST"), []byte("hello")); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	raw[len(raw)-1] ^= 0xFF // corrupt last byte of CRC
	_, _, err := readChunk(bytes.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), "crc mismatch") {
		t.Fatalf("expected crc mismatch error, got %v", err)
	}
}

func TestChunkLengthCapNonDLTA(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{'F', 'A', 'K', 'E'})
	binary.Write(&buf, binary.BigEndian, uint32(16<<20+1))
	_, _, err := readChunk(&buf)
	if err == nil || !strings.Contains(err.Error(), "16 MiB") {
		t.Fatalf("expected length-cap error, got %v", err)
	}
}

func TestChunkLengthCapDLTAExempt(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{'D', 'L', 'T', 'A'})
	binary.Write(&buf, binary.BigEndian, uint32(16<<20+1))
	_, _, err := readChunk(&buf)
	// DLTA bypasses the cap, so the failure mode is "ran out of bytes"
	if err == nil || strings.Contains(err.Error(), "16 MiB") {
		t.Fatalf("DLTA should bypass length cap, got %v", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected io.ErrUnexpectedEOF reading DLTA payload, got %v", err)
	}
}
```

- [ ] **Step 2: Run tests, confirm they fail with "undefined: writeChunk / readChunk / fourcc"**

Run: `go test -run TestChunk -v ./...`
Expected: build failure citing the three undefined symbols.

- [ ] **Step 3: Implement `chunks.go`**

```go
package main

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
)

// chunkLengthCap is the maximum payload length accepted for any chunk
// other than DLTA. Matches MAME CHD's metadata cap and prevents
// malloc(garbage) on a corrupt-but-CRC-valid hostile payload. DLTA is
// exempt because the delta payload is genuinely large.
const chunkLengthCap = 16 << 20 // 16 MiB

var crc32Table = crc32.IEEETable

// fourcc converts a 4-character ASCII string to a [4]byte at compile
// time. Panics if s is not exactly 4 bytes — only used with literal
// constants like "MFST", "TRKS", "HASH", "DLTA".
func fourcc(s string) [4]byte {
	if len(s) != 4 {
		panic(fmt.Sprintf("fourcc: %q is not 4 bytes", s))
	}
	var t [4]byte
	copy(t[:], s)
	return t
}

// dltaTag is the one tag exempt from the length cap.
var dltaTag = fourcc("DLTA")

// writeChunk emits a chunk: tag(4) + length(4 BE) + payload + CRC32(4 BE).
// CRC is computed over (tag || payload).
func writeChunk(w io.Writer, tag [4]byte, payload []byte) error {
	if _, err := w.Write(tag[:]); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, uint32(len(payload))); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	h := crc32.New(crc32Table)
	h.Write(tag[:])
	h.Write(payload)
	return binary.Write(w, binary.BigEndian, h.Sum32())
}

// readChunk reads one chunk and returns (tag, payload, err).
// On clean EOF before any byte is read, returns (_, _, io.EOF).
// On any partial read, wraps io.ErrUnexpectedEOF.
// Rejects length > chunkLengthCap for any tag other than DLTA.
func readChunk(r io.Reader) ([4]byte, []byte, error) {
	var head [8]byte
	n, err := io.ReadFull(r, head[:])
	if err == io.EOF && n == 0 {
		return [4]byte{}, nil, io.EOF
	}
	if err != nil {
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			return [4]byte{}, nil, fmt.Errorf("reading chunk header: %w", io.ErrUnexpectedEOF)
		}
		return [4]byte{}, nil, fmt.Errorf("reading chunk header: %w", err)
	}
	var tag [4]byte
	copy(tag[:], head[:4])
	length := binary.BigEndian.Uint32(head[4:8])
	if tag != dltaTag && int(length) > chunkLengthCap {
		return tag, nil, fmt.Errorf("chunk %q length %d exceeds 16 MiB cap", tag, length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return tag, nil, fmt.Errorf("reading chunk %q payload: %w", tag, io.ErrUnexpectedEOF)
		}
		return tag, nil, err
	}
	var crcBuf [4]byte
	if _, err := io.ReadFull(r, crcBuf[:]); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return tag, nil, fmt.Errorf("reading chunk %q crc: %w", tag, io.ErrUnexpectedEOF)
		}
		return tag, nil, err
	}
	wantCRC := binary.BigEndian.Uint32(crcBuf[:])
	h := crc32.New(crc32Table)
	h.Write(tag[:])
	h.Write(payload)
	if h.Sum32() != wantCRC {
		return tag, nil, fmt.Errorf("chunk %q crc mismatch", tag)
	}
	return tag, payload, nil
}
```

- [ ] **Step 4: Run tests, confirm they pass**

Run: `go test -run TestChunk -v ./...`
Expected: PASS for all eight chunk-primitive tests.

- [ ] **Step 5: Commit**

```bash
git add chunks.go chunks_test.go
git commit -m "$(cat <<'EOF'
feat: chunk-framing primitives (chunks.go)

writeChunk emits tag(4) + length(4 BE) + payload + CRC32(4 BE) over
(tag || payload), CRC-32/IEEE polynomial. readChunk inverts. 16 MiB
length cap on every tag other than DLTA, mirroring MAME CHD's
metadata cap to defend against malloc(garbage) on a corrupt-but-CRC-
valid hostile payload.

Foundation for the v2 chunk-based container format. Manifest, track
table, hash records, and zlib delta will each become one chunk in
subsequent tasks.
EOF
)"
```

---

## Task 2: Manifest struct migration — `CreatedUnix int64`

**Goal:** Migrate `Manifest.CreatedUTC string` → `CreatedUnix int64`. Drop the `runtime.Version()` suffix from `tool_version`. Existing JSON-based `WriteContainer`/`ReadContainer` continue to work through the next four tasks until the chunk-based replacement lands in Task 6.

**Files:**
- Modify: `manifest.go:23-30` (Manifest struct)
- Modify: `pack.go:146-147` (tool_version + created_utc construction)
- Modify: `inspect.go` (display formatting for the new int64 field)
- Modify: `manifest_test.go` (round-trip fixture uses int64)
- Modify: `inspect_test.go` (assertions on display format if they reference the date)

**Acceptance Criteria:**
- [ ] `Manifest.CreatedUnix` is `int64` with json tag `"created_unix"`; `CreatedUTC` is gone
- [ ] `pack.go` records `time.Now().UTC().Unix()`, and `ToolVersion: toolVersion` (no `runtime.Version()` suffix)
- [ ] `inspect.go` formats `CreatedUnix` back to RFC3339 for human display: `time.Unix(m.CreatedUnix, 0).UTC().Format(time.RFC3339)`
- [ ] All existing tests pass (build is green; round-trip still works through the JSON path)
- [ ] `runtime` import is removed from `pack.go` if it has no other uses

**Verify:** `go build ./... && go test -count=1 ./...` → all tests pass; `grep runtime.Version /home/hugh/miniscram/*.go` returns no hits

**Steps:**

- [ ] **Step 1: Update Manifest struct in `manifest.go`**

```go
// Manifest is the metadata embedded in every v2 .miniscram container.
// (Currently still serialized via JSON; chunk encoding lands in Task 6.)
type Manifest struct {
	ToolVersion      string    `json:"tool_version"`
	CreatedUnix      int64     `json:"created_unix"`
	WriteOffsetBytes int       `json:"write_offset_bytes"`
	LeadinLBA        int32     `json:"leadin_lba"`
	Scram            ScramInfo `json:"scram"`
	Tracks           []Track   `json:"tracks"`
}
```

- [ ] **Step 2: Update `pack.go` manifest construction**

Locate `pack.go:146-147` (the `Manifest{...}` literal in the Pack function) and replace those two lines:

```go
		ToolVersion:      toolVersion,
		CreatedUnix:      time.Now().UTC().Unix(),
```

Then remove the `"runtime"` import from `pack.go` if `runtime.Version()` was its only use.

- [ ] **Step 3: Update `inspect.go` display**

Find the line printing `created_utc` (around `inspect.go:30`). Replace:

```go
	fmt.Fprintf(&b, "  created_utc:            %s\n", m.CreatedUTC)
```

with:

```go
	fmt.Fprintf(&b, "  created_utc:            %s\n", time.Unix(m.CreatedUnix, 0).UTC().Format(time.RFC3339))
```

Add `"time"` to `inspect.go`'s imports if not already present.

- [ ] **Step 4: Update test fixtures**

In `manifest_test.go:13-29` (the `TestContainerRoundtrip` Manifest literal) and any other places setting `CreatedUTC: "..."`, replace with:

```go
		CreatedUnix: 1714435200, // arbitrary fixed UTC seconds
```

Search for any other references:

```bash
grep -rn 'CreatedUTC\|created_utc\|runtime\.Version' /home/hugh/miniscram/*.go
```

Update each callsite mechanically.

- [ ] **Step 5: Run full test suite**

Run: `go build ./... && go test -count=1 ./...`
Expected: PASS. JSON round-trip still works because the field rename is symmetric across writer and reader.

- [ ] **Step 6: Commit**

```bash
git add manifest.go pack.go inspect.go manifest_test.go inspect_test.go
git commit -m "$(cat <<'EOF'
refactor: CreatedUTC string → CreatedUnix int64; drop runtime suffix

Two pre-flight changes for the v2 chunk format:
- Manifest.CreatedUTC (RFC3339 string) → Manifest.CreatedUnix (int64
  seconds since epoch). Display formatting moves to inspect's print
  site.
- Drop the "(go1.x.y)" suffix from tool_version. Forensics noise that
  doesn't affect output bytes.

Round-trip JSON path still works; chunk replacement lands in Task 6.
EOF
)"
```

---

## Task 3: MFST payload codec

**Goal:** Implement `encodeMFSTPayload(*Manifest) []byte` and `decodeMFSTPayload([]byte) (*Manifest, error)` matching the spec's `MFST` layout.

**Files:**
- Modify: `chunks.go` (add `mfstTag`, `encodeMFSTPayload`, `decodeMFSTPayload`)
- Modify: `chunks_test.go` (add `TestMFSTRoundTrip`, `TestMFSTRejectsTruncated`)

**Acceptance Criteria:**
- [ ] `encodeMFSTPayload` writes: `tool_version_len(uint16 BE) || tool_version(UTF-8) || created_unix(int64 BE) || write_offset_bytes(int32 BE) || leadin_lba(int32 BE) || scram_size(int64 BE)`
- [ ] `decodeMFSTPayload` inverts it; populates a partial `Manifest` (only the MFST scalar fields; Tracks/Scram populated by other codecs)
- [ ] Round-trip preserves all five fields byte-equal
- [ ] Truncated payload (any prefix shorter than expected) → wrapped `io.ErrUnexpectedEOF`
- [ ] `tool_version` is decoded as UTF-8 (no validation; if needed in the future, add `utf8.Valid` check)

**Verify:** `go test -run TestMFST -v ./...` → all MFST tests pass

**Steps:**

- [ ] **Step 1: Write the failing tests**

Append to `chunks_test.go`:

```go
func TestMFSTRoundTrip(t *testing.T) {
	in := &Manifest{
		ToolVersion:      "miniscram 1.0.0-test",
		CreatedUnix:      1714435200,
		WriteOffsetBytes: -48,
		LeadinLBA:        -45150,
		Scram:            ScramInfo{Size: 897527784},
	}
	payload := encodeMFSTPayload(in)
	out, err := decodeMFSTPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	if out.ToolVersion != in.ToolVersion ||
		out.CreatedUnix != in.CreatedUnix ||
		out.WriteOffsetBytes != in.WriteOffsetBytes ||
		out.LeadinLBA != in.LeadinLBA ||
		out.Scram.Size != in.Scram.Size {
		t.Fatalf("round-trip mismatch:\nin:  %+v\nout: %+v", in, out)
	}
}

func TestMFSTRejectsTruncated(t *testing.T) {
	full := encodeMFSTPayload(&Manifest{
		ToolVersion: "miniscram", CreatedUnix: 1, WriteOffsetBytes: 0,
		LeadinLBA: 0, Scram: ScramInfo{Size: 0},
	})
	for i := 0; i < len(full); i++ {
		_, err := decodeMFSTPayload(full[:i])
		if err == nil {
			t.Fatalf("decoding truncated MFST (len=%d) should fail", i)
		}
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("len=%d: expected ErrUnexpectedEOF, got %v", i, err)
		}
	}
}
```

- [ ] **Step 2: Run tests, confirm fail with "undefined: encodeMFSTPayload / decodeMFSTPayload"**

Run: `go test -run TestMFST -v ./...`

- [ ] **Step 3: Implement codec in `chunks.go`**

Add to `chunks.go`:

```go
var (
	mfstTag = fourcc("MFST")
	trksTag = fourcc("TRKS")
	hashTag = fourcc("HASH")
)

// encodeMFSTPayload emits the MFST chunk payload per spec §"MFST":
// tool_version_len(uint16 BE) || tool_version(UTF-8) ||
// created_unix(int64 BE) || write_offset_bytes(int32 BE) ||
// leadin_lba(int32 BE) || scram_size(int64 BE).
func encodeMFSTPayload(m *Manifest) []byte {
	var b []byte
	tv := []byte(m.ToolVersion)
	b = binary.BigEndian.AppendUint16(b, uint16(len(tv)))
	b = append(b, tv...)
	b = binary.BigEndian.AppendUint64(b, uint64(m.CreatedUnix))
	b = binary.BigEndian.AppendUint32(b, uint32(int32(m.WriteOffsetBytes)))
	b = binary.BigEndian.AppendUint32(b, uint32(m.LeadinLBA))
	b = binary.BigEndian.AppendUint64(b, uint64(m.Scram.Size))
	return b
}

// decodeMFSTPayload inverts encodeMFSTPayload. Populates only the
// MFST scalar fields on the returned Manifest.
func decodeMFSTPayload(payload []byte) (*Manifest, error) {
	r := payloadReader{buf: payload}
	tvLen, err := r.uint16()
	if err != nil {
		return nil, fmt.Errorf("MFST tool_version_len: %w", err)
	}
	tv, err := r.bytes(int(tvLen))
	if err != nil {
		return nil, fmt.Errorf("MFST tool_version: %w", err)
	}
	created, err := r.uint64()
	if err != nil {
		return nil, fmt.Errorf("MFST created_unix: %w", err)
	}
	wo, err := r.uint32()
	if err != nil {
		return nil, fmt.Errorf("MFST write_offset_bytes: %w", err)
	}
	lba, err := r.uint32()
	if err != nil {
		return nil, fmt.Errorf("MFST leadin_lba: %w", err)
	}
	ss, err := r.uint64()
	if err != nil {
		return nil, fmt.Errorf("MFST scram_size: %w", err)
	}
	return &Manifest{
		ToolVersion:      string(tv),
		CreatedUnix:      int64(created),
		WriteOffsetBytes: int(int32(wo)),
		LeadinLBA:        int32(lba),
		Scram:            ScramInfo{Size: int64(ss)},
	}, nil
}

// payloadReader is a thin cursor over a byte slice that returns
// io.ErrUnexpectedEOF on any short read, with helper methods for
// the integer widths the codecs use.
type payloadReader struct {
	buf []byte
	pos int
}

func (r *payloadReader) bytes(n int) ([]byte, error) {
	if r.pos+n > len(r.buf) {
		return nil, io.ErrUnexpectedEOF
	}
	out := r.buf[r.pos : r.pos+n]
	r.pos += n
	return out, nil
}

func (r *payloadReader) uint8() (uint8, error) {
	b, err := r.bytes(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (r *payloadReader) uint16() (uint16, error) {
	b, err := r.bytes(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

func (r *payloadReader) uint32() (uint32, error) {
	b, err := r.bytes(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

func (r *payloadReader) uint64() (uint64, error) {
	b, err := r.bytes(8)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(b), nil
}

func (r *payloadReader) eof() bool { return r.pos == len(r.buf) }
```

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test -run TestMFST -v ./...`

- [ ] **Step 5: Commit**

```bash
git add chunks.go chunks_test.go
git commit -m "feat: MFST payload codec — manifest scalars as fixed binary"
```

---

## Task 4: TRKS payload codec

**Goal:** Implement `encodeTRKSPayload([]Track) []byte` / `decodeTRKSPayload([]byte) ([]Track, error)` matching the spec's `TRKS` layout.

**Files:**
- Modify: `chunks.go` (add `encodeTRKSPayload`, `decodeTRKSPayload`)
- Modify: `chunks_test.go` (add `TestTRKSRoundTrip`)

**Acceptance Criteria:**
- [ ] `encodeTRKSPayload` writes: `count(uint16 BE) || (per track) number(uint8) || mode_len(uint8) || mode(ASCII) || first_lba(int32 BE) || size(int64 BE) || filename_len(uint16 BE) || filename(UTF-8)`
- [ ] `decodeTRKSPayload` inverts it; per-track `Hashes` field is left zero (populated by `decodeHASHPayload` in Task 5)
- [ ] Round-trip with a 2-track fixture (data + audio) preserves all fields
- [ ] Truncated payload at any byte boundary → wrapped `io.ErrUnexpectedEOF`

**Verify:** `go test -run TestTRKS -v ./...` → all TRKS tests pass

**Steps:**

- [ ] **Step 1: Write the failing tests**

Append to `chunks_test.go`:

```go
func TestTRKSRoundTrip(t *testing.T) {
	in := []Track{
		{Number: 1, Mode: "MODE1/2352", FirstLBA: 0, Size: 791104608, Filename: "x.bin"},
		{Number: 2, Mode: "AUDIO", FirstLBA: 336420, Size: 47040, Filename: "x.bin"},
	}
	payload := encodeTRKSPayload(in)
	out, err := decodeTRKSPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("count: got %d want %d", len(out), len(in))
	}
	for i := range in {
		// Hashes intentionally not compared — populated by HASH codec.
		got, want := out[i], in[i]
		if got.Number != want.Number || got.Mode != want.Mode ||
			got.FirstLBA != want.FirstLBA || got.Size != want.Size ||
			got.Filename != want.Filename {
			t.Errorf("track %d:\ngot:  %+v\nwant: %+v", i, got, want)
		}
	}
}

func TestTRKSRejectsTruncated(t *testing.T) {
	full := encodeTRKSPayload([]Track{{Number: 1, Mode: "AUDIO", FirstLBA: 0, Size: 0, Filename: "t.bin"}})
	for i := 0; i < len(full); i++ {
		_, err := decodeTRKSPayload(full[:i])
		if err == nil {
			t.Errorf("decoding truncated TRKS (len=%d) should fail", i)
		}
	}
}
```

- [ ] **Step 2: Run tests, confirm fail**

Run: `go test -run TestTRKS -v ./...`

- [ ] **Step 3: Implement codec in `chunks.go`**

Append to `chunks.go`:

```go
// encodeTRKSPayload emits the TRKS chunk payload per spec §"TRKS".
// Per-track Hashes are emitted in the HASH chunk, not here.
func encodeTRKSPayload(tracks []Track) []byte {
	var b []byte
	b = binary.BigEndian.AppendUint16(b, uint16(len(tracks)))
	for _, t := range tracks {
		mode := []byte(t.Mode)
		filename := []byte(t.Filename)
		b = append(b, byte(t.Number), byte(len(mode)))
		b = append(b, mode...)
		b = binary.BigEndian.AppendUint32(b, uint32(t.FirstLBA))
		b = binary.BigEndian.AppendUint64(b, uint64(t.Size))
		b = binary.BigEndian.AppendUint16(b, uint16(len(filename)))
		b = append(b, filename...)
	}
	return b
}

// decodeTRKSPayload inverts encodeTRKSPayload. Per-track Hashes are
// left zero; HASH chunk populates them.
func decodeTRKSPayload(payload []byte) ([]Track, error) {
	r := payloadReader{buf: payload}
	count, err := r.uint16()
	if err != nil {
		return nil, fmt.Errorf("TRKS count: %w", err)
	}
	tracks := make([]Track, count)
	for i := range tracks {
		num, err := r.uint8()
		if err != nil {
			return nil, fmt.Errorf("TRKS track[%d] number: %w", i, err)
		}
		modeLen, err := r.uint8()
		if err != nil {
			return nil, fmt.Errorf("TRKS track[%d] mode_len: %w", i, err)
		}
		mode, err := r.bytes(int(modeLen))
		if err != nil {
			return nil, fmt.Errorf("TRKS track[%d] mode: %w", i, err)
		}
		firstLBA, err := r.uint32()
		if err != nil {
			return nil, fmt.Errorf("TRKS track[%d] first_lba: %w", i, err)
		}
		size, err := r.uint64()
		if err != nil {
			return nil, fmt.Errorf("TRKS track[%d] size: %w", i, err)
		}
		fnLen, err := r.uint16()
		if err != nil {
			return nil, fmt.Errorf("TRKS track[%d] filename_len: %w", i, err)
		}
		fn, err := r.bytes(int(fnLen))
		if err != nil {
			return nil, fmt.Errorf("TRKS track[%d] filename: %w", i, err)
		}
		tracks[i] = Track{
			Number:   int(num),
			Mode:     string(mode),
			FirstLBA: int32(firstLBA),
			Size:     int64(size),
			Filename: string(fn),
		}
	}
	if !r.eof() {
		return nil, fmt.Errorf("TRKS: %d trailing bytes after %d tracks", len(payload)-r.pos, count)
	}
	return tracks, nil
}
```

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test -run TestTRKS -v ./...`

- [ ] **Step 5: Commit**

```bash
git add chunks.go chunks_test.go
git commit -m "feat: TRKS payload codec — track table as fixed binary"
```

---

## Task 5: HASH payload codec

**Goal:** Implement `encodeHASHPayload(*Manifest) []byte` and `decodeHASHPayload([]byte, *Manifest) error` per spec's `HASH` layout. The decoder mutates an existing `Manifest` (which already has its `Tracks` populated by `decodeTRKSPayload`).

**Files:**
- Modify: `chunks.go` (add `encodeHASHPayload`, `decodeHASHPayload`, `algoTag`/`hashAlgo` helpers)
- Modify: `chunks_test.go` (add `TestHASHRoundTrip`)

**Acceptance Criteria:**
- [ ] `encodeHASHPayload` emits: `count(uint16 BE) || (per record) target(uint8) || algo([4]byte ASCII) || digest_len(uint8) || digest(bytes)`
- [ ] Algorithm tags: `MD5 ` (note trailing space), `SHA1`, `S256`. Digest lengths: 16, 20, 32 respectively
- [ ] Encoder emits one record per (file × algorithm), with `target=0` for scram and `target=i` (1-based) for track i
- [ ] Decoder populates `m.Scram.Hashes` (when target=0) and `m.Tracks[target-1].Hashes` (when target>0)
- [ ] Round-trip with a manifest containing scram + 2 tracks × 3 algos preserves every digest byte-equal
- [ ] Decoder rejects unknown `algo` tag with a clear error
- [ ] Decoder rejects `digest_len` not matching the algorithm's expected length
- [ ] Decoder rejects `target` out of range (target > len(m.Tracks))

**Verify:** `go test -run TestHASH -v ./...` → all HASH tests pass

**Steps:**

- [ ] **Step 1: Write the failing tests**

Append to `chunks_test.go`:

```go
func TestHASHRoundTrip(t *testing.T) {
	in := &Manifest{
		Scram: ScramInfo{Hashes: FileHashes{
			MD5:    "0123456789abcdef0123456789abcdef",
			SHA1:   "0123456789abcdef0123456789abcdef01234567",
			SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}},
		Tracks: []Track{
			{Number: 1, Hashes: FileHashes{
				MD5:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				SHA1:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			}},
		},
	}
	payload := encodeHASHPayload(in)
	out := &Manifest{Tracks: []Track{{Number: 1}}}
	if err := decodeHASHPayload(payload, out); err != nil {
		t.Fatal(err)
	}
	if out.Scram.Hashes != in.Scram.Hashes {
		t.Errorf("scram hashes mismatch:\ngot:  %+v\nwant: %+v", out.Scram.Hashes, in.Scram.Hashes)
	}
	if out.Tracks[0].Hashes != in.Tracks[0].Hashes {
		t.Errorf("track[0] hashes mismatch")
	}
}

func TestHASHRejectsUnknownAlgo(t *testing.T) {
	var b []byte
	b = binary.BigEndian.AppendUint16(b, 1)
	b = append(b, 0)               // target=0
	b = append(b, 'X', 'X', 'X', 'X') // bogus algo
	b = append(b, 16)              // claims 16-byte digest
	b = append(b, make([]byte, 16)...)
	err := decodeHASHPayload(b, &Manifest{})
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("expected unknown-algo error, got %v", err)
	}
}

func TestHASHRejectsBadDigestLen(t *testing.T) {
	var b []byte
	b = binary.BigEndian.AppendUint16(b, 1)
	b = append(b, 0)
	b = append(b, 'M', 'D', '5', ' ')
	b = append(b, 99) // wrong length for MD5 (16)
	b = append(b, make([]byte, 99)...)
	err := decodeHASHPayload(b, &Manifest{})
	if err == nil || !strings.Contains(err.Error(), "digest length") {
		t.Fatalf("expected digest-length error, got %v", err)
	}
}

func TestHASHRejectsBadTarget(t *testing.T) {
	var b []byte
	b = binary.BigEndian.AppendUint16(b, 1)
	b = append(b, 5)               // target=5 with 0 tracks
	b = append(b, 'M', 'D', '5', ' ')
	b = append(b, 16)
	b = append(b, make([]byte, 16)...)
	err := decodeHASHPayload(b, &Manifest{})
	if err == nil || !strings.Contains(err.Error(), "target") {
		t.Fatalf("expected target out-of-range error, got %v", err)
	}
}
```

- [ ] **Step 2: Run tests, confirm fail**

Run: `go test -run TestHASH -v ./...`

- [ ] **Step 3: Implement codec in `chunks.go`**

Append to `chunks.go`:

```go
// hashAlgoLen maps the on-disk algo tag to its expected digest length.
var hashAlgoLen = map[[4]byte]int{
	fourcc("MD5 "): 16,
	fourcc("SHA1"): 20,
	fourcc("S256"): 32,
}

// encodeHASHPayload emits the HASH chunk payload per spec §"HASH".
// One record per (file × algorithm).
func encodeHASHPayload(m *Manifest) []byte {
	var b []byte
	count := uint16((1 + len(m.Tracks)) * 3) // scram + tracks, each × MD5/SHA1/SHA256
	b = binary.BigEndian.AppendUint16(b, count)

	emit := func(target uint8, h FileHashes) {
		b = appendHashRecord(b, target, fourcc("MD5 "), h.MD5, 16)
		b = appendHashRecord(b, target, fourcc("SHA1"), h.SHA1, 20)
		b = appendHashRecord(b, target, fourcc("S256"), h.SHA256, 32)
	}
	emit(0, m.Scram.Hashes)
	for i, t := range m.Tracks {
		emit(uint8(i+1), t.Hashes)
	}
	return b
}

// appendHashRecord parses the hex string and appends one record.
// Panics if the hex doesn't decode to exactly digestLen bytes — only
// called from encodeHASHPayload, where lengths are invariants.
func appendHashRecord(b []byte, target uint8, algo [4]byte, hexDigest string, digestLen int) []byte {
	digest, err := hex.DecodeString(hexDigest)
	if err != nil || len(digest) != digestLen {
		panic(fmt.Sprintf("HASH encode: bad %v digest %q (decode err %v, len %d, want %d)",
			algo, hexDigest, err, len(digest), digestLen))
	}
	b = append(b, target)
	b = append(b, algo[:]...)
	b = append(b, byte(digestLen))
	b = append(b, digest...)
	return b
}

// decodeHASHPayload reads the HASH chunk and populates Hashes fields
// on the supplied Manifest. m.Tracks must already be sized to match
// what encodeHASHPayload produced (i.e., decodeTRKSPayload ran first).
func decodeHASHPayload(payload []byte, m *Manifest) error {
	r := payloadReader{buf: payload}
	count, err := r.uint16()
	if err != nil {
		return fmt.Errorf("HASH count: %w", err)
	}
	for i := uint16(0); i < count; i++ {
		target, err := r.uint8()
		if err != nil {
			return fmt.Errorf("HASH record[%d] target: %w", i, err)
		}
		algoBytes, err := r.bytes(4)
		if err != nil {
			return fmt.Errorf("HASH record[%d] algo: %w", i, err)
		}
		var algo [4]byte
		copy(algo[:], algoBytes)
		want, ok := hashAlgoLen[algo]
		if !ok {
			return fmt.Errorf("HASH record[%d]: unknown algo %q", i, algo)
		}
		digestLen, err := r.uint8()
		if err != nil {
			return fmt.Errorf("HASH record[%d] digest_len: %w", i, err)
		}
		if int(digestLen) != want {
			return fmt.Errorf("HASH record[%d] %q: digest length %d, want %d", i, algo, digestLen, want)
		}
		digest, err := r.bytes(int(digestLen))
		if err != nil {
			return fmt.Errorf("HASH record[%d] digest: %w", i, err)
		}
		hexDigest := hex.EncodeToString(digest)
		var dest *FileHashes
		switch {
		case target == 0:
			dest = &m.Scram.Hashes
		case int(target) <= len(m.Tracks):
			dest = &m.Tracks[target-1].Hashes
		default:
			return fmt.Errorf("HASH record[%d]: target %d out of range (have %d tracks)", i, target, len(m.Tracks))
		}
		switch algo {
		case fourcc("MD5 "):
			dest.MD5 = hexDigest
		case fourcc("SHA1"):
			dest.SHA1 = hexDigest
		case fourcc("S256"):
			dest.SHA256 = hexDigest
		}
	}
	if !r.eof() {
		return fmt.Errorf("HASH: %d trailing bytes after %d records", len(payload)-r.pos, count)
	}
	return nil
}
```

Add `"encoding/hex"` to `chunks.go`'s imports.

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test -run TestHASH -v ./...`

- [ ] **Step 5: Commit**

```bash
git add chunks.go chunks_test.go
git commit -m "feat: HASH payload codec — tagged records (target, algo, digest)"
```

---

## Task 6: WriteContainer / ReadContainer rewrite

**Goal:** Replace `WriteContainer` and `ReadContainer` with chunk-based implementations. The header is now 5 bytes (`magic + version`), followed by `MFST`, `TRKS`, `HASH`, `DLTA` chunks. Drop the JSON encoding path entirely.

**Files:**
- Modify: `manifest.go` (rewrite `WriteContainer` / `ReadContainer` bodies; remove `Marshal`; drop `containerHeaderSize` const since chunks are length-prefixed; drop `encoding/json`, `encoding/binary` if no other uses; keep `compress/zlib`)
- Modify: `manifest_test.go` (the byte-level layout tests `TestContainerDeltaIsZlibFramed` and `TestContainerRejectsPlaintextDelta` — adapt or fold into Task 7)

**Acceptance Criteria:**
- [ ] `WriteContainer(path, m, deltaSrc)` writes header + MFST + TRKS + HASH + DLTA, in that order
- [ ] DLTA's payload is the zlib-compressed delta stream; `WriteContainer` zlib-encodes `deltaSrc` into a buffer first (since the DLTA chunk needs its length up-front)
- [ ] `ReadContainer(path)` returns `(*Manifest, []byte, error)` with the manifest fully populated (MFST + TRKS + HASH merged) and the delta bytes (zlib-decoded)
- [ ] `ReadContainer` rejects non-`MSCM` magic, non-`0x02` version, missing/duplicate critical chunks, MFST not first, unknown critical chunks
- [ ] `ReadContainer` accepts (skips) unknown lowercase chunks
- [ ] Existing `TestContainerRoundtrip` passes
- [ ] All e2e tests (`e2e_test.go`, `verify_test.go`, `unpack_test.go`) pass

**Verify:** `go test -count=1 ./...` → all tests pass

**Steps:**

- [ ] **Step 1: Update `manifest.go` constants and Manifest struct comment**

Replace the const block in `manifest.go`:

```go
const (
	containerMagic   = "MSCM"
	containerVersion = byte(0x02) // v2
)

// fileHeaderSize is magic(4) + version(1).
const fileHeaderSize = 5
```

Drop `containerHeaderSize` (no longer meaningful — chunks are length-prefixed).

Update the Manifest doc comment to drop the "JSON metadata" framing (it's binary now). Drop the `Marshal` method (no longer used).

- [ ] **Step 2: Rewrite `WriteContainer`**

Replace the `WriteContainer` body:

```go
// WriteContainer writes a v2 .miniscram file at path: 5-byte header
// (magic + version) followed by MFST, TRKS, HASH, DLTA chunks.
// Atomic: writes to a .tmp file then renames.
func WriteContainer(path string, m *Manifest, deltaSrc io.Reader) error {
	// Compress the delta into memory first — DLTA's chunk length must
	// be known up-front, and the delta is small (KiB to low MiB)
	// relative to scram (hundreds of MiB).
	var dltaBuf bytes.Buffer
	zw, err := zlib.NewWriterLevel(&dltaBuf, zlib.BestCompression)
	if err != nil {
		return fmt.Errorf("creating zlib writer: %w", err)
	}
	if _, err := io.Copy(zw, deltaSrc); err != nil {
		return fmt.Errorf("compressing delta payload: %w", err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("flushing zlib writer: %w", err)
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
	if err := writeChunk(f, mfstTag, encodeMFSTPayload(m)); err != nil {
		return fmt.Errorf("writing MFST: %w", err)
	}
	if err := writeChunk(f, trksTag, encodeTRKSPayload(m.Tracks)); err != nil {
		return fmt.Errorf("writing TRKS: %w", err)
	}
	if err := writeChunk(f, hashTag, encodeHASHPayload(m)); err != nil {
		return fmt.Errorf("writing HASH: %w", err)
	}
	if err := writeChunk(f, dltaTag, dltaBuf.Bytes()); err != nil {
		return fmt.Errorf("writing DLTA: %w", err)
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

Add `"bytes"` to imports if not present.

- [ ] **Step 3: Rewrite `ReadContainer`**

Replace the `ReadContainer` body:

```go
// ReadContainer parses a v2 .miniscram file and returns its manifest
// and the (zlib-decoded) raw delta bytes.
func ReadContainer(path string) (*Manifest, []byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	var head [fileHeaderSize]byte
	if _, err := io.ReadFull(f, head[:]); err != nil {
		return nil, nil, fmt.Errorf("reading file header: %w", err)
	}
	if string(head[:4]) != containerMagic {
		return nil, nil, fmt.Errorf("not a miniscram container (bad magic %q)", head[:4])
	}
	if head[4] != containerVersion {
		return nil, nil, fmt.Errorf(
			"container version 0x%02x; this build only reads v2.\nrebuild miniscram from a matching commit:\nhttps://github.com/hughobrien/miniscram",
			head[4])
	}

	var (
		m            *Manifest
		dlta         []byte
		seen         = map[[4]byte]int{}
		firstChunk   [4]byte
		firstChunkOK bool
	)
	for {
		tag, payload, err := readChunk(f)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if !firstChunkOK {
			firstChunk = tag
			firstChunkOK = true
		}
		seen[tag]++
		if seen[tag] > 1 && isCritical(tag) {
			return nil, nil, fmt.Errorf("duplicate chunk %q", tag)
		}
		switch tag {
		case mfstTag:
			m, err = decodeMFSTPayload(payload)
			if err != nil {
				return nil, nil, err
			}
		case trksTag:
			tracks, err := decodeTRKSPayload(payload)
			if err != nil {
				return nil, nil, err
			}
			if m == nil {
				m = &Manifest{}
			}
			m.Tracks = tracks
		case hashTag:
			if m == nil || m.Tracks == nil {
				return nil, nil, fmt.Errorf("HASH chunk before MFST/TRKS")
			}
			if err := decodeHASHPayload(payload, m); err != nil {
				return nil, nil, err
			}
		case dltaTag:
			zr, err := zlib.NewReader(bytes.NewReader(payload))
			if err != nil {
				return nil, nil, fmt.Errorf("decompressing delta payload: %w", err)
			}
			dlta, err = io.ReadAll(zr)
			zr.Close()
			if err != nil {
				return nil, nil, fmt.Errorf("decompressing delta payload: %w", err)
			}
		default:
			if isCritical(tag) {
				return nil, nil, fmt.Errorf("unsupported critical chunk %q", tag)
			}
			// Lowercase first letter — ancillary, skip silently.
		}
	}
	if firstChunkOK && firstChunk != mfstTag {
		return nil, nil, fmt.Errorf("MFST must be the first chunk")
	}
	for _, required := range [][4]byte{mfstTag, trksTag, hashTag, dltaTag} {
		if seen[required] == 0 {
			return nil, nil, fmt.Errorf("missing required chunk %q", required)
		}
	}
	return m, dlta, nil
}

// isCritical reports whether a chunk's first byte is uppercase ASCII.
// Per spec, uppercase = critical (must be understood), lowercase =
// ancillary (readers may skip).
func isCritical(tag [4]byte) bool {
	return tag[0] >= 'A' && tag[0] <= 'Z'
}
```

Add `"errors"` to imports if not present.

- [ ] **Step 4: Adapt `TestContainerDeltaIsZlibFramed`**

The old test verified that bytes immediately after the JSON manifest start with zlib magic 0x78. Rewrite to verify the same property of the DLTA chunk's payload:

```go
func TestContainerDeltaIsZlibFramed(t *testing.T) {
	m := &Manifest{
		ToolVersion: "miniscram-test",
		CreatedUnix: 1714435200,
		Scram:       ScramInfo{Size: 0, Hashes: FileHashes{MD5: strings.Repeat("0", 32), SHA1: strings.Repeat("0", 40), SHA256: strings.Repeat("0", 64)}},
		Tracks: []Track{{
			Number: 1, Mode: "MODE1/2352", FirstLBA: 0, Size: 0, Filename: "t.bin",
			Hashes: FileHashes{MD5: strings.Repeat("0", 32), SHA1: strings.Repeat("0", 40), SHA256: strings.Repeat("0", 64)},
		}},
	}
	delta := bytes.Repeat([]byte("ABCDEFGH"), 1024)
	path := filepath.Join(t.TempDir(), "x.miniscram")
	if err := WriteContainer(path, m, bytes.NewReader(delta)); err != nil {
		t.Fatal(err)
	}
	// Walk chunks; find DLTA; check its payload starts with 0x78.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(raw[fileHeaderSize:])
	for {
		tag, payload, err := readChunk(r)
		if err != nil {
			t.Fatalf("walking chunks: %v", err)
		}
		if tag == dltaTag {
			if len(payload) < 2 || payload[0] != 0x78 {
				t.Fatalf("DLTA payload should start with zlib magic 0x78, got 0x%02x", payload[0])
			}
			if len(payload) >= len(delta) {
				t.Fatalf("compression no-op: DLTA payload %d bytes vs plaintext %d bytes", len(payload), len(delta))
			}
			return
		}
	}
}
```

- [ ] **Step 5: Drop `TestContainerRejectsPlaintextDelta`**

This test synthesized a v1 byte layout to verify zlib detection. The corruption-rejection coverage in Task 7 subsumes it. Delete the function entirely.

- [ ] **Step 6: Run full test suite**

Run: `go build ./... && go test -count=1 ./...`
Expected: all tests pass. Round-trip, e2e synthetic, verify, unpack tests should all be green.

If `TestContainerRoundtrip` is still using `CreatedUTC` after Task 2's mass-edit, fix it now to use `CreatedUnix`.

- [ ] **Step 7: Commit**

```bash
git add manifest.go manifest_test.go
git commit -m "$(cat <<'EOF'
feat: chunk-based WriteContainer / ReadContainer (v2)

Replaces v1's JSON manifest framing with the chunk format defined in
the v2 spec: 5-byte file header (magic + version) followed by MFST,
TRKS, HASH, DLTA chunks. Each chunk is length-prefixed and CRC32-
protected via writeChunk/readChunk.

Reader walks chunks until EOF, validates each critical chunk appears
exactly once with MFST first, rejects unknown uppercase tags, accepts
(skips) unknown lowercase tags. Hard error on any version != 0x02.

DLTA payload is the zlib-compressed delta verbatim; its length
prefix delimits the delta exactly, no read-to-EOF heuristic.
EOF
)"
```

---

## Task 7: Corruption rejection tests

**Goal:** Cover every rejection path enumerated in the spec's "Error handling" and "Read behavior" sections with a discrete sub-test in `TestContainerRejectsCorruption`.

**Files:**
- Modify: `manifest_test.go` (add `TestContainerRejectsCorruption`)

**Acceptance Criteria:** Each named rejection path is exercised by a sub-test and produces an error matching the relevant text:
- [ ] `bad-magic` — file starts with `"BADM"` instead of `"MSCM"`
- [ ] `wrong-version-v1` — magic OK, version byte 0x01 → "container version 0x01"
- [ ] `wrong-version-v3` — magic OK, version byte 0x03
- [ ] `wrong-version-v9` — magic OK, version byte 0x09
- [ ] `truncated-mid-chunk` — header + partial MFST chunk → wraps `io.ErrUnexpectedEOF`
- [ ] `bad-crc` — flip one bit in MFST's CRC trailer → "crc mismatch"
- [ ] `length-cap-exceeded` — synthesize a non-DLTA chunk with `length = 16<<20 + 1` → "16 MiB"
- [ ] `unknown-critical-chunk` — synthesize a valid `XXXX` chunk after MFST → "unsupported critical chunk"
- [ ] `unknown-ancillary-chunk` — synthesize a valid `xxxx` chunk; container still parses successfully
- [ ] `missing-mfst` — emit only TRKS + HASH + DLTA → "missing required chunk \"MFST\""
- [ ] `missing-trks` — emit MFST + HASH + DLTA → "missing required chunk \"TRKS\""
- [ ] `missing-hash` — emit MFST + TRKS + DLTA → "missing required chunk \"HASH\""
- [ ] `missing-dlta` — emit MFST + TRKS + HASH → "missing required chunk \"DLTA\""
- [ ] `duplicate-mfst` — emit two MFST chunks → "duplicate chunk \"MFST\""
- [ ] `mfst-not-first` — emit TRKS first, then MFST → "MFST must be the first chunk"

**Verify:** `go test -run TestContainerRejectsCorruption -v ./...` → all sub-tests pass

**Steps:**

- [ ] **Step 1: Add a fixture builder helper to `manifest_test.go`**

The corruption tests need to synthesize valid base containers and selectively damage them. Add a helper that returns the byte-level layout of a valid v2 container:

```go
// validV2Container builds a minimal valid v2 container in memory and
// returns its bytes. Used as a base by corruption tests, which mutate
// specific bytes/chunks and assert the reader rejects.
func validV2Container(t *testing.T) []byte {
	t.Helper()
	m := &Manifest{
		ToolVersion: "miniscram-test",
		CreatedUnix: 1,
		Scram:       ScramInfo{Size: 0, Hashes: FileHashes{MD5: strings.Repeat("0", 32), SHA1: strings.Repeat("0", 40), SHA256: strings.Repeat("0", 64)}},
		Tracks: []Track{{
			Number: 1, Mode: "AUDIO", FirstLBA: 0, Size: 0, Filename: "t.bin",
			Hashes: FileHashes{MD5: strings.Repeat("0", 32), SHA1: strings.Repeat("0", 40), SHA256: strings.Repeat("0", 64)},
		}},
	}
	path := filepath.Join(t.TempDir(), "valid.miniscram")
	if err := WriteContainer(path, m, bytes.NewReader([]byte{0, 0, 0, 0})); err != nil {
		t.Fatalf("writing valid container: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// chunkRange locates the [start, end) byte range of the named chunk
// inside a container blob (post-header). Returns (-1, -1) if not found.
func chunkRange(t *testing.T, raw []byte, want [4]byte) (int, int) {
	t.Helper()
	off := fileHeaderSize
	for off < len(raw) {
		var tag [4]byte
		copy(tag[:], raw[off:off+4])
		length := int(binary.BigEndian.Uint32(raw[off+4 : off+8]))
		end := off + 8 + length + 4 // type + length + payload + CRC
		if tag == want {
			return off, end
		}
		off = end
	}
	return -1, -1
}

// writeRaw writes raw bytes to a tempfile and returns the path.
func writeRaw(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "corrupt.miniscram")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
```

- [ ] **Step 2: Add the corruption test**

Append to `manifest_test.go`:

```go
func TestContainerRejectsCorruption(t *testing.T) {
	t.Run("bad-magic", func(t *testing.T) {
		raw := validV2Container(t)
		copy(raw[:4], []byte("BADM"))
		_, _, err := ReadContainer(writeRaw(t, raw))
		if err == nil || !strings.Contains(err.Error(), "bad magic") {
			t.Fatalf("expected bad-magic error, got %v", err)
		}
	})

	for _, ver := range []byte{0x01, 0x03, 0x09} {
		t.Run(fmt.Sprintf("wrong-version-0x%02x", ver), func(t *testing.T) {
			raw := validV2Container(t)
			raw[4] = ver
			_, _, err := ReadContainer(writeRaw(t, raw))
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("0x%02x", ver)) {
				t.Fatalf("expected version-0x%02x error, got %v", ver, err)
			}
			if !strings.Contains(err.Error(), "v2") {
				t.Errorf("expected error to mention 'v2', got %v", err)
			}
		})
	}

	t.Run("truncated-mid-chunk", func(t *testing.T) {
		raw := validV2Container(t)
		// Truncate halfway through MFST.
		mfstStart, mfstEnd := chunkRange(t, raw, mfstTag)
		_, _, err := ReadContainer(writeRaw(t, raw[:mfstStart+(mfstEnd-mfstStart)/2]))
		if err == nil {
			t.Fatal("expected error reading truncated MFST")
		}
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("expected io.ErrUnexpectedEOF, got %v", err)
		}
	})

	t.Run("bad-crc", func(t *testing.T) {
		raw := validV2Container(t)
		_, mfstEnd := chunkRange(t, raw, mfstTag)
		raw[mfstEnd-1] ^= 0xFF // flip last byte of MFST's CRC
		_, _, err := ReadContainer(writeRaw(t, raw))
		if err == nil || !strings.Contains(err.Error(), "crc mismatch") {
			t.Fatalf("expected crc-mismatch error, got %v", err)
		}
	})

	t.Run("length-cap-exceeded", func(t *testing.T) {
		raw := validV2Container(t)
		// Inject a forged FAKE chunk after MFST with length > 16 MiB.
		_, mfstEnd := chunkRange(t, raw, mfstTag)
		var fake bytes.Buffer
		fake.Write([]byte{'F', 'A', 'K', 'E'})
		binary.Write(&fake, binary.BigEndian, uint32(16<<20+1))
		// No payload bytes — readChunk should reject before reading them.
		corrupt := append(append([]byte{}, raw[:mfstEnd]...), fake.Bytes()...)
		corrupt = append(corrupt, raw[mfstEnd:]...)
		_, _, err := ReadContainer(writeRaw(t, corrupt))
		if err == nil || !strings.Contains(err.Error(), "16 MiB") {
			t.Fatalf("expected 16-MiB-cap error, got %v", err)
		}
	})

	t.Run("unknown-critical-chunk", func(t *testing.T) {
		raw := validV2Container(t)
		_, mfstEnd := chunkRange(t, raw, mfstTag)
		// Inject a valid XXXX chunk (with correct CRC) after MFST.
		var bogus bytes.Buffer
		writeChunk(&bogus, fourcc("XXXX"), []byte("payload"))
		corrupt := append(append([]byte{}, raw[:mfstEnd]...), bogus.Bytes()...)
		corrupt = append(corrupt, raw[mfstEnd:]...)
		_, _, err := ReadContainer(writeRaw(t, corrupt))
		if err == nil || !strings.Contains(err.Error(), "unsupported critical chunk") {
			t.Fatalf("expected unsupported-critical-chunk error, got %v", err)
		}
	})

	t.Run("unknown-ancillary-chunk-accepted", func(t *testing.T) {
		raw := validV2Container(t)
		_, mfstEnd := chunkRange(t, raw, mfstTag)
		var bogus bytes.Buffer
		writeChunk(&bogus, fourcc("xfut"), []byte("future-data"))
		corrupt := append(append([]byte{}, raw[:mfstEnd]...), bogus.Bytes()...)
		corrupt = append(corrupt, raw[mfstEnd:]...)
		if _, _, err := ReadContainer(writeRaw(t, corrupt)); err != nil {
			t.Fatalf("ancillary chunk should be skipped, got %v", err)
		}
	})

	for _, missing := range []struct {
		name string
		tag  [4]byte
	}{
		{"missing-mfst", mfstTag},
		{"missing-trks", trksTag},
		{"missing-hash", hashTag},
		{"missing-dlta", dltaTag},
	} {
		t.Run(missing.name, func(t *testing.T) {
			raw := validV2Container(t)
			start, end := chunkRange(t, raw, missing.tag)
			corrupt := append(append([]byte{}, raw[:start]...), raw[end:]...)
			_, _, err := ReadContainer(writeRaw(t, corrupt))
			if err == nil || !strings.Contains(err.Error(), "missing required chunk") {
				t.Fatalf("expected missing-required-chunk error, got %v", err)
			}
			if !strings.Contains(err.Error(), string(missing.tag[:])) {
				t.Errorf("expected error to name %q, got %v", missing.tag, err)
			}
		})
	}

	t.Run("duplicate-mfst", func(t *testing.T) {
		raw := validV2Container(t)
		mfstStart, mfstEnd := chunkRange(t, raw, mfstTag)
		mfstChunk := append([]byte{}, raw[mfstStart:mfstEnd]...)
		// Insert a second copy of MFST right after the first.
		corrupt := append(append([]byte{}, raw[:mfstEnd]...), mfstChunk...)
		corrupt = append(corrupt, raw[mfstEnd:]...)
		_, _, err := ReadContainer(writeRaw(t, corrupt))
		if err == nil || !strings.Contains(err.Error(), "duplicate chunk") {
			t.Fatalf("expected duplicate-chunk error, got %v", err)
		}
	})

	t.Run("mfst-not-first", func(t *testing.T) {
		raw := validV2Container(t)
		mfstStart, mfstEnd := chunkRange(t, raw, mfstTag)
		trksStart, trksEnd := chunkRange(t, raw, trksTag)
		// Swap MFST and TRKS so TRKS appears first.
		mfst := append([]byte{}, raw[mfstStart:mfstEnd]...)
		trks := append([]byte{}, raw[trksStart:trksEnd]...)
		corrupt := append([]byte{}, raw[:mfstStart]...)
		corrupt = append(corrupt, trks...)
		corrupt = append(corrupt, mfst...)
		corrupt = append(corrupt, raw[trksEnd:]...)
		_, _, err := ReadContainer(writeRaw(t, corrupt))
		if err == nil || !strings.Contains(err.Error(), "MFST must be the first chunk") {
			t.Fatalf("expected MFST-not-first error, got %v", err)
		}
	})
}
```

- [ ] **Step 2 (verify): Run tests**

Run: `go test -run TestContainerRejectsCorruption -v ./...`
Expected: all sub-tests pass (15 total).

- [ ] **Step 3: Commit**

```bash
git add manifest_test.go
git commit -m "$(cat <<'EOF'
test: corruption rejection — every v2 read-time error path

Covers each named error path from the spec: bad magic, wrong version
(v1/v3/v9), mid-chunk truncation, CRC mismatch, length cap exceeded,
unknown critical chunk, unknown ancillary chunk (must accept), each
of the four missing-required-chunk cases, duplicate critical chunk,
MFST not first.
EOF
)"
```

---

## Task 8: Refresh `e2e_redump_test.go` per-fixture bounds

**Goal:** The new chunk format adds modest overhead per chunk (4-byte type + 4-byte length + 4-byte CRC = 12 bytes per chunk × 4 chunks = 48 bytes baseline) plus removes the variable-length JSON manifest (which was several hundred bytes). Net per-fixture container size shifts. Refresh the bounds in `e2e_redump_test.go` so they reflect actual v2 sizes.

**Files:**
- Modify: `e2e_redump_test.go`

**Acceptance Criteria:**
- [ ] Each fixture row's `containerSizeBound` reflects an actual measured v2 container size with a small headroom (e.g., +5%) for legitimate variation
- [ ] `go test -tags redump_data ./...` passes for every available fixture
- [ ] If no fixtures are present in the worktree (`test-discs/` empty), document this in the commit and leave bounds at conservative pre-rebase values (the test is gated on `redump_data` tag and skipped without fixtures)

**Verify:** `go test -tags redump_data -count=1 ./...` → fixture e2e tests pass (or are skipped if no fixtures present)

**Steps:**

- [ ] **Step 1: Locate the bound table**

```bash
grep -n "containerSizeBound\|deltaSize\|errorCount" /home/hugh/miniscram/e2e_redump_test.go | head -20
```

This will show the per-fixture row structure.

- [ ] **Step 2: Run e2e against available fixtures**

```bash
ls /home/hugh/miniscram/test-discs/ 2>/dev/null
go test -tags redump_data -v -run TestE2ERoundTripRealDiscs ./...
```

If no fixtures: skip to Step 4 with the commit message noting that bounds were left at pre-rebase values.

- [ ] **Step 3: For each fixture present, measure**

The test's failure mode (when bounds are wrong) prints actual vs. allowed. Read those off, set new bounds to `actual * 1.05` rounded up to a clean number. Update the table in `e2e_redump_test.go`.

- [ ] **Step 4: Commit**

```bash
git add e2e_redump_test.go
git commit -m "test: refresh e2e_redump container-size bounds for v2 format"
```

---

## Self-review

- **Spec coverage**: every section of the spec has a task — chunk framing (Task 1), each chunk type (Tasks 3-5), header/version logic (Task 6), every named rejection path (Task 7), Manifest field migration (Task 2), e2e bounds (Task 8). ✓
- **Placeholders scanned**: no TBDs, no "implement appropriate error handling" — all error wrapping is shown inline. ✓
- **Type consistency**: `[4]byte` used for tags throughout; `payloadReader` is the single cursor abstraction; `Manifest` field names align between Tasks 2-7. ✓
- **TDD ordering**: every task that adds code starts with a failing test (Step 1), confirms it fails (Step 2), implements (Step 3), confirms pass (Step 4), commits (Step 5). ✓
