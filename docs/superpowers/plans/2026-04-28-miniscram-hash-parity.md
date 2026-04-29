# miniscram Hash Parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement TASKS.md item C1 — record md5 and sha1 alongside sha256 for both `.bin` and `.scram` in every miniscram manifest, computed in a single I/O pass; verify all three on unpack/verify with strict any-of-three-mismatch policy; bump container format_version 2→3.

**Architecture:** A new `hashFile` helper in `pack.go` returns a `FileHashes` struct (md5 + sha1 + sha256) computed via `io.MultiWriter`. A `compareHashes` helper produces a per-hash diff message on any mismatch. Pack populates 6 manifest fields (3 hashes × 2 files); Unpack and Verify recompute and compare via the same helpers, wrapping mismatches in renamed sentinels (`errBinHashMismatch`, `errOutputHashMismatch`). Container version byte bumps `0x02 → 0x03` in lockstep with `FormatVersion 2 → 3`. v2 containers are rejected with the same migration-error pattern v1 used.

**Tech Stack:** Go stdlib only (`crypto/md5`, `crypto/sha1`, `crypto/sha256`, `encoding/hex`, `io.MultiWriter`). No new dependencies.

**Variance from spec:** None.

---

## File Structure

| File | Role |
| --- | --- |
| `pack.go` (modify) | Add `FileHashes`, `hashFile`, `compareHashes`. Replace `sha256File` callers with `hashFile`+`compareHashes`. Bump `FormatVersion` to 3. Populate 6 hash fields in manifest. Update `verifyRoundTrip`. Delete `sha256File` once last caller migrated. |
| `manifest.go` (modify) | Add `BinMD5`, `BinSHA1`, `ScramMD5`, `ScramSHA1` fields. Bump `containerVersion` to `0x03`. Update v0.2→v0.3 migration error message. |
| `unpack.go` (modify) | Rename sentinels. Replace `sha256File` calls with `hashFile`+`compareHashes`. Update reporter step labels. |
| `verify.go` (modify) | Replace `sha256File` call with `hashFile`+`compareHashes`. Update reporter step label. |
| `main.go` (modify) | Update `errors.Is` references in three exit-code switches. |
| `inspect.go` (modify) | Add 4 `fmt.Fprintf` lines to `formatHumanInspect` (md5/sha1 lines for both files). |
| `pack_test.go` (modify) | Add `TestHashFile_*`, `TestCompareHashes_*`, `TestPackPopulatesAllSixHashFields`. |
| `unpack_test.go` (modify) | Rename sentinel references (if any). Add per-hash tampering tests. |
| `verify_test.go` (modify) | Rename `errBinSHA256Mismatch`→`errBinHashMismatch` and `errOutputSHA256Mismatch`→`errOutputHashMismatch`. Add per-hash tampering tests. |
| `inspect_test.go` (modify) | Update `TestCLIInspectRejectsV1` analog → `TestCLIInspectRejectsV2` (or add alongside). Add `TestInspectShowsAllSixHashes`, `TestInspectJSONIncludesAllSixHashes`. |

Three tasks. Task 1 is foundational (helper + unit tests). Task 2 is the integration big-bang (manifest + pack + unpack + verify + main + per-hash tamper tests). Task 3 surfaces the new fields in inspect output.

---

## Task 1: hashFile + FileHashes + compareHashes helpers

**Goal:** Add the foundational hash helpers with unit tests proving they compute correct hashes single-pass and detect any-of-three mismatches with a clear per-hash diff message. `sha256File` stays in place; integration is Task 2.

**Files:**
- Modify: `pack.go`
- Modify: `pack_test.go`

**Acceptance Criteria:**
- [ ] `FileHashes` struct with MD5, SHA1, SHA256 string fields exported (within package).
- [ ] `hashFile(path string) (FileHashes, error)` returns lowercase hex md5/sha1/sha256 in a single I/O pass; opens path, streams through `io.MultiWriter`, closes.
- [ ] `compareHashes(got, want FileHashes) error` returns nil iff all three match; otherwise an unwrapped error with per-hash diff message (which hash mismatched, got vs expected).
- [ ] Unit tests:
  - `TestHashFile_EmptyFile` — empty file returns known-vector md5 (`d41d8cd98f00b204e9800998ecf8427e`), sha1 (`da39a3ee5e6b4b0d3255bfef95601890afd80709`), sha256 (`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`).
  - `TestHashFile_NonemptyFile` — small known file content matches independent computation.
  - `TestHashFile_OpenError` — nonexistent path returns the os.Open error.
  - `TestCompareHashes_AllMatch` — same hashes returns nil.
  - `TestCompareHashes_OneMismatch` × 3 (one per hash) — returns non-nil error whose message names the mismatched hash.
  - `TestCompareHashes_AllMismatch` — all three differ; message reports all three.

**Verify:** `go test ./... -run "TestHashFile|TestCompareHashes" -v && go vet ./...`

**Steps:**

- [ ] **Step 1: Write the failing tests in `pack_test.go`**

Append to `/home/hugh/miniscram/pack_test.go`:

```go
func TestHashFile_EmptyFile(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(tmp, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := hashFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	const (
		emptyMD5    = "d41d8cd98f00b204e9800998ecf8427e"
		emptySHA1   = "da39a3ee5e6b4b0d3255bfef95601890afd80709"
		emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	)
	if got.MD5 != emptyMD5 {
		t.Errorf("MD5 = %s; want %s", got.MD5, emptyMD5)
	}
	if got.SHA1 != emptySHA1 {
		t.Errorf("SHA1 = %s; want %s", got.SHA1, emptySHA1)
	}
	if got.SHA256 != emptySHA256 {
		t.Errorf("SHA256 = %s; want %s", got.SHA256, emptySHA256)
	}
}

func TestHashFile_NonemptyFile(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "abc")
	if err := os.WriteFile(tmp, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := hashFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	// Reference values from FIPS-180 / RFC 1321 test vectors for "abc".
	if got.MD5 != "900150983cd24fb0d6963f7d28e17f72" {
		t.Errorf("MD5 = %s", got.MD5)
	}
	if got.SHA1 != "a9993e364706816aba3e25717850c26c9cd0d89d" {
		t.Errorf("SHA1 = %s", got.SHA1)
	}
	if got.SHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("SHA256 = %s", got.SHA256)
	}
}

func TestHashFile_OpenError(t *testing.T) {
	_, err := hashFile("/nonexistent/path/here")
	if err == nil {
		t.Fatal("expected error opening nonexistent file")
	}
}

func TestCompareHashes_AllMatch(t *testing.T) {
	h := FileHashes{MD5: "aaa", SHA1: "bbb", SHA256: "ccc"}
	if err := compareHashes(h, h); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestCompareHashes_MD5Mismatch(t *testing.T) {
	got := FileHashes{MD5: "aaa", SHA1: "bbb", SHA256: "ccc"}
	want := FileHashes{MD5: "xxx", SHA1: "bbb", SHA256: "ccc"}
	err := compareHashes(got, want)
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if !strings.Contains(err.Error(), "md5") {
		t.Errorf("error message missing 'md5': %v", err)
	}
}

func TestCompareHashes_SHA1Mismatch(t *testing.T) {
	got := FileHashes{MD5: "aaa", SHA1: "bbb", SHA256: "ccc"}
	want := FileHashes{MD5: "aaa", SHA1: "yyy", SHA256: "ccc"}
	err := compareHashes(got, want)
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if !strings.Contains(err.Error(), "sha1") {
		t.Errorf("error message missing 'sha1': %v", err)
	}
}

func TestCompareHashes_SHA256Mismatch(t *testing.T) {
	got := FileHashes{MD5: "aaa", SHA1: "bbb", SHA256: "ccc"}
	want := FileHashes{MD5: "aaa", SHA1: "bbb", SHA256: "zzz"}
	err := compareHashes(got, want)
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if !strings.Contains(err.Error(), "sha256") {
		t.Errorf("error message missing 'sha256': %v", err)
	}
}

func TestCompareHashes_AllMismatch(t *testing.T) {
	got := FileHashes{MD5: "aaa", SHA1: "bbb", SHA256: "ccc"}
	want := FileHashes{MD5: "xxx", SHA1: "yyy", SHA256: "zzz"}
	err := compareHashes(got, want)
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	msg := err.Error()
	for _, want := range []string{"md5", "sha1", "sha256"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %v", want, err)
		}
	}
}
```

If `pack_test.go` doesn't already import `strings`, add it.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run "TestHashFile|TestCompareHashes" -v`
Expected: build failure — `undefined: hashFile`, `undefined: FileHashes`, `undefined: compareHashes`.

- [ ] **Step 3: Add helpers to `pack.go`**

In `/home/hugh/miniscram/pack.go`:

a) Add imports `"crypto/md5"` and `"crypto/sha1"` to the import block (sha256 and hex are already imported).

b) Add the helpers immediately after the existing `sha256File` function (around line 204):

```go
// FileHashes holds the three hashes miniscram records per file.
type FileHashes struct {
	MD5    string
	SHA1   string
	SHA256 string
}

// hashFile streams path through MD5, SHA-1, and SHA-256 in a single
// I/O pass and returns all three as lowercase hex.
func hashFile(path string) (FileHashes, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileHashes{}, err
	}
	defer f.Close()
	m, s1, s256 := md5.New(), sha1.New(), sha256.New()
	w := io.MultiWriter(m, s1, s256)
	if _, err := io.Copy(w, f); err != nil {
		return FileHashes{}, err
	}
	return FileHashes{
		MD5:    hex.EncodeToString(m.Sum(nil)),
		SHA1:   hex.EncodeToString(s1.Sum(nil)),
		SHA256: hex.EncodeToString(s256.Sum(nil)),
	}, nil
}

// compareHashes returns nil iff all three hashes match. Otherwise it
// returns a plain (un-sentinel-wrapped) error whose message describes
// each hash's status. Callers wrap with their own sentinel via
// fmt.Errorf("%w: %v", sentinel, err) to attach the appropriate exit
// code semantics.
func compareHashes(got, want FileHashes) error {
	var diffs []string
	if got.MD5 != want.MD5 {
		diffs = append(diffs, fmt.Sprintf("md5 got %s, manifest expects %s", got.MD5, want.MD5))
	}
	if got.SHA1 != want.SHA1 {
		diffs = append(diffs, fmt.Sprintf("sha1 got %s, manifest expects %s", got.SHA1, want.SHA1))
	}
	if got.SHA256 != want.SHA256 {
		diffs = append(diffs, fmt.Sprintf("sha256 got %s, manifest expects %s", got.SHA256, want.SHA256))
	}
	if len(diffs) == 0 {
		return nil
	}
	return errors.New(strings.Join(diffs, "; "))
}
```

If `pack.go` doesn't already import `errors` and `strings`, add them.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -run "TestHashFile|TestCompareHashes" -v`
Expected: PASS — all 7 helper tests.

- [ ] **Step 5: Run full suite + vet**

Run: `go test ./... && go vet ./...`
Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add pack.go pack_test.go
git commit -m "$(cat <<'EOF'
pack: add hashFile + FileHashes + compareHashes helpers

Foundation for C1 (hash algorithm parity with Redumper). hashFile
streams a file through MD5/SHA-1/SHA-256 in a single I/O pass via
io.MultiWriter; compareHashes returns nil on all-match or a plain
error with a per-hash diff message that callers wrap in their
appropriate sentinel.

sha256File is preserved in this commit for the existing call sites
(pack/unpack/verify); Task 2 of the C1 cycle migrates them and
deletes sha256File.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Manifest fields, version bump, integrate hashFile across pack/unpack/verify

**Goal:** Add 4 hash fields to Manifest, bump container format from v0.2 to v0.3 (both byte and JSON `format_version`), rename SHA256-named sentinels to Hash-named, replace all `sha256File` call sites with `hashFile`+`compareHashes`, delete `sha256File`. Add per-hash tampering tests proving any single hash mismatch is caught.

**Files:**
- Modify: `manifest.go`
- Modify: `pack.go`
- Modify: `unpack.go`
- Modify: `verify.go`
- Modify: `main.go`
- Modify: `pack_test.go` (TestPackPopulatesAllSixHashFields)
- Modify: `unpack_test.go` (no sentinel renames needed — existing tests use bare error checks)
- Modify: `verify_test.go` (rename `errBinSHA256Mismatch`→`errBinHashMismatch` and `errOutputSHA256Mismatch`→`errOutputHashMismatch` in errors.Is calls; add per-hash tampering tests)
- Modify: `inspect_test.go` (rename `TestCLIInspectRejectsV1` analog if needed; add v0.2→v0.3 rejection coverage)

**Acceptance Criteria:**
- [ ] Manifest struct has BinMD5, BinSHA1, BinSHA256 (existing), ScramMD5, ScramSHA1, ScramSHA256 (existing) — fields grouped per file, weakest-to-strongest.
- [ ] `containerVersion` = `0x03`. `Manifest.FormatVersion` = `3` (set in pack.go).
- [ ] ReadContainer rejects a hand-built v0.2 container with new migration error message ("v0.2 .miniscram files cannot be read directly by this build — re-pack from the original .bin").
- [ ] Sentinels renamed: `errBinHashMismatch`, `errOutputHashMismatch`. `errVerifyMismatch` unchanged.
- [ ] All 6 hash fields populated after `Pack`; values match a separate-pass recompute via `hashFile`.
- [ ] Per-hash tampering matrix passes (FL-style synthetic test mutates one hash field and asserts the right sentinel + exit code):
  - bin md5 tamper → `errBinHashMismatch`, exit 5
  - bin sha1 tamper → `errBinHashMismatch`, exit 5
  - bin sha256 tamper → `errBinHashMismatch`, exit 5
  - scram md5 tamper → `errOutputHashMismatch`, exit 3
  - scram sha1 tamper → `errOutputHashMismatch`, exit 3
  - scram sha256 tamper → `errOutputHashMismatch`, exit 3
- [ ] Same matrix via Verify (errOutputHashMismatch, exit 3).
- [ ] `sha256File` is removed from the codebase (no callers, no definition).
- [ ] `go test ./...` PASS, `go vet ./...` clean.

**Verify:** `go test ./... -v && go vet ./...`

**Steps:**

- [ ] **Step 1: Update `manifest.go` — add fields, bump version byte, update message**

In `/home/hugh/miniscram/manifest.go`:

a) Change `containerVersion` constant:
```go
containerVersion    = byte(0x03)
```

b) Update the `Manifest` struct's hash field list. Replace lines 23–26 (the existing scram_size→bin_sha256 block) with:

```go
	ScramSize     int64  `json:"scram_size"`
	ScramMD5      string `json:"scram_md5"`
	ScramSHA1     string `json:"scram_sha1"`
	ScramSHA256   string `json:"scram_sha256"`
	BinSize       int64  `json:"bin_size"`
	BinMD5        string `json:"bin_md5"`
	BinSHA1       string `json:"bin_sha1"`
	BinSHA256     string `json:"bin_sha256"`
```

c) Update the version-mismatch error in `ReadContainer` (around line 119). Replace the existing message with:

```go
		return nil, nil, fmt.Errorf("unsupported container version 0x%02x (this build expects 0x%02x); "+
			"v0.2 .miniscram files cannot be read directly by this build — re-pack from the original .bin",
			header[4], containerVersion)
```

- [ ] **Step 2: Update `pack.go` — populate hashes, FormatVersion=3, verifyRoundTrip, drop sha256File**

In `/home/hugh/miniscram/pack.go`:

a) Replace the bin-hash block (around line 109). The existing code is:

```go
	binSHA, err := sha256File(opts.BinPath)
	if err != nil {
		return err
	}
```

Replace with:

```go
	binHashes, err := hashFile(opts.BinPath)
	if err != nil {
		return err
	}
```

b) Replace the scram-hash block (around line 117) the same way:

```go
	scramHashes, err := hashFile(opts.ScramPath)
	if err != nil {
		return err
	}
```

c) Update the manifest construction (around line 140). Find the existing manifest literal that includes `FormatVersion: 2`, `ScramSHA256: scramSHA`, `BinSHA256: binSHA`. Update to:

```go
		FormatVersion:        3,
		ToolVersion:          toolVersion,
		CreatedUTC:           time.Now().UTC().Format(time.RFC3339),
		ScramSize:            scramSize,
		ScramMD5:             scramHashes.MD5,
		ScramSHA1:            scramHashes.SHA1,
		ScramSHA256:          scramHashes.SHA256,
		BinSize:              binSize,
		BinMD5:               binHashes.MD5,
		BinSHA1:              binHashes.SHA1,
		BinSHA256:            binHashes.SHA256,
		WriteOffsetBytes:     writeOffsetBytes,
```

(Keep the rest of the literal — LeadinLBA, Tracks, BinFirstLBA, etc. — exactly as it was.)

d) Update `verifyRoundTrip` (around line 470). The existing code is:

```go
	got, err := sha256File(tmpOutPath)
	if err != nil {
		return err
	}
	if got != want.ScramSHA256 {
		return fmt.Errorf("%w: round-trip sha256 %s != recorded %s", errVerifyMismatch, got, want.ScramSHA256)
	}
	return nil
```

Replace with:

```go
	got, err := hashFile(tmpOutPath)
	if err != nil {
		return err
	}
	wantHashes := FileHashes{MD5: want.ScramMD5, SHA1: want.ScramSHA1, SHA256: want.ScramSHA256}
	if err := compareHashes(got, wantHashes); err != nil {
		return fmt.Errorf("%w: round-trip hash mismatch: %v", errVerifyMismatch, err)
	}
	return nil
```

e) Delete the entire `sha256File` function (lines 193–204).

f) Remove the now-unused imports if Go compilation flags them — `crypto/sha256` and `encoding/hex` are still used by `hashFile` so they stay; only check that no orphaned imports remain.

- [ ] **Step 3: Update `unpack.go` — rename sentinels, replace sha256File calls**

In `/home/hugh/miniscram/unpack.go`:

a) Rename the sentinels at the top of the file:

```go
var (
	errBinHashMismatch    = errors.New("bin hash mismatch")
	errOutputHashMismatch = errors.New("output hash mismatch")
)
```

b) Replace the bin-verification block (around line 56–67). The existing code:

```go
	st = r.Step("verifying bin sha256")
	binSHA, err := sha256File(opts.BinPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	if binSHA != m.BinSHA256 {
		err := fmt.Errorf("%w: got %s, manifest expects %s", errBinSHA256Mismatch, binSHA, m.BinSHA256)
		st.Fail(err)
		return err
	}
	st.Done("matches")
```

Replace with:

```go
	st = r.Step("verifying bin hashes")
	binHashes, err := hashFile(opts.BinPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	wantBin := FileHashes{MD5: m.BinMD5, SHA1: m.BinSHA1, SHA256: m.BinSHA256}
	if err := compareHashes(binHashes, wantBin); err != nil {
		err := fmt.Errorf("%w: %v", errBinHashMismatch, err)
		st.Fail(err)
		return err
	}
	st.Done("all three match")
```

c) Replace the output-verification block (around line 154–166). The existing code:

```go
	st = r.Step("verifying output sha256")
	outSHA, err := sha256File(opts.OutputPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	if outSHA != m.ScramSHA256 {
		_ = os.Remove(opts.OutputPath)
		err := fmt.Errorf("%w: output %s, manifest %s", errOutputSHA256Mismatch, outSHA, m.ScramSHA256)
		st.Fail(err)
		return err
	}
	st.Done("matches")
```

Replace with:

```go
	st = r.Step("verifying output hashes")
	outHashes, err := hashFile(opts.OutputPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	wantOut := FileHashes{MD5: m.ScramMD5, SHA1: m.ScramSHA1, SHA256: m.ScramSHA256}
	if err := compareHashes(outHashes, wantOut); err != nil {
		_ = os.Remove(opts.OutputPath)
		err := fmt.Errorf("%w: %v", errOutputHashMismatch, err)
		st.Fail(err)
		return err
	}
	st.Done("all three match")
```

- [ ] **Step 4: Update `verify.go`**

In `/home/hugh/miniscram/verify.go`, replace the sha256 check block (around lines 58–69):

```go
	st := r.Step("verifying scram sha256")
	got, err := sha256File(tmpPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	if got != m.ScramSHA256 {
		err := fmt.Errorf("%w: computed %s, manifest %s", errOutputSHA256Mismatch, got, m.ScramSHA256)
		st.Fail(err)
		return err
	}
	st.Done("matches")
	return nil
```

Replace with:

```go
	st := r.Step("verifying scram hashes")
	got, err := hashFile(tmpPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	wantHashes := FileHashes{MD5: m.ScramMD5, SHA1: m.ScramSHA1, SHA256: m.ScramSHA256}
	if err := compareHashes(got, wantHashes); err != nil {
		err := fmt.Errorf("%w: %v", errOutputHashMismatch, err)
		st.Fail(err)
		return err
	}
	st.Done("all three match")
	return nil
```

- [ ] **Step 5: Update `main.go` exit-code switches**

In `/home/hugh/miniscram/main.go`, update three functions:

a) `packErrorToExit` — replace `errBinSHA256Mismatch` with `errBinHashMismatch` and `errOutputSHA256Mismatch` with `errOutputHashMismatch`:

```go
func packErrorToExit(err error) int {
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

b) `unpackErrorToExit`:

```go
func unpackErrorToExit(err error) int {
	switch {
	case errors.Is(err, errBinHashMismatch):
		return exitWrongBin
	case errors.Is(err, errOutputHashMismatch):
		return exitVerifyFail
	default:
		return exitIO
	}
}
```

c) `verifyErrorToExit`:

```go
func verifyErrorToExit(err error) int {
	switch {
	case errors.Is(err, errBinHashMismatch):
		return exitWrongBin
	case errors.Is(err, errOutputHashMismatch):
		return exitVerifyFail
	default:
		return exitIO
	}
}
```

- [ ] **Step 6: Update `verify_test.go` — sentinel renames + per-hash tampering tests**

In `/home/hugh/miniscram/verify_test.go`:

a) Rename two `errors.Is` calls in existing tests:
- `TestVerifyDetectsScramSHA256Mismatch`: `errors.Is(err, errOutputSHA256Mismatch)` → `errors.Is(err, errOutputHashMismatch)`. Update test name to `TestVerifyDetectsScramHashMismatch` (since it now covers any-of-three semantics, though it still tampers sha256 specifically).
- `TestVerifyDetectsWrongBin`: `errors.Is(err, errBinSHA256Mismatch)` → `errors.Is(err, errBinHashMismatch)`.

b) Append the per-hash tampering matrix tests:

```go
// TestVerifyDetectsScramHashMismatchAllThree confirms the strict
// any-of-three policy: tampering ANY single recorded scram hash in
// the container's manifest causes Verify to fail with errOutputHashMismatch,
// not just sha256 mismatches.
func TestVerifyDetectsScramHashMismatchAllThree(t *testing.T) {
	for _, hashName := range []string{"scram_md5", "scram_sha1", "scram_sha256"} {
		t.Run(hashName, func(t *testing.T) {
			binPath, containerPath, _, m := packForVerify(t)

			// Identify which manifest hex string to tamper.
			var target string
			switch hashName {
			case "scram_md5":
				target = m.ScramMD5
			case "scram_sha1":
				target = m.ScramSHA1
			case "scram_sha256":
				target = m.ScramSHA256
			}

			data, err := os.ReadFile(containerPath)
			if err != nil {
				t.Fatal(err)
			}
			idx := bytes.Index(data, []byte(target))
			if idx < 0 {
				t.Fatalf("hash %q (%s) not found in container", hashName, target)
			}
			data[idx] ^= 1
			if err := os.WriteFile(containerPath, data, 0o644); err != nil {
				t.Fatal(err)
			}

			err = Verify(VerifyOptions{
				BinPath: binPath, ContainerPath: containerPath,
			}, NewReporter(io.Discard, true))
			if !errors.Is(err, errOutputHashMismatch) {
				t.Fatalf("expected errOutputHashMismatch tampering %s, got %v", hashName, err)
			}
		})
	}
}
```

- [ ] **Step 7: Update `unpack_test.go` (sentinel checks if any)**

In `/home/hugh/miniscram/unpack_test.go`, search for `errBinSHA256Mismatch` and `errOutputSHA256Mismatch`. The existing tests use bare error-non-nil checks, so most likely no rename is needed; but if any references appear, rename them mechanically.

- [ ] **Step 8: Update `pack_test.go` — add TestPackPopulatesAllSixHashFields**

Append to `/home/hugh/miniscram/pack_test.go`:

```go
func TestPackPopulatesAllSixHashFields(t *testing.T) {
	binPath, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, 0, 10)
	containerPath := filepath.Join(dir, "x.miniscram")
	if err := Pack(PackOptions{
		BinPath: binPath, CuePath: cuePath, ScramPath: scramPath,
		OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, NewReporter(io.Discard, true)); err != nil {
		t.Fatal(err)
	}
	m, _, err := ReadContainer(containerPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]string{
		"BinMD5":      m.BinMD5,
		"BinSHA1":     m.BinSHA1,
		"BinSHA256":   m.BinSHA256,
		"ScramMD5":    m.ScramMD5,
		"ScramSHA1":   m.ScramSHA1,
		"ScramSHA256": m.ScramSHA256,
	} {
		if got == "" {
			t.Errorf("%s is empty in manifest", name)
		}
	}
	// Cross-check: recompute bin hashes via hashFile and compare.
	fresh, err := hashFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.MD5 != m.BinMD5 || fresh.SHA1 != m.BinSHA1 || fresh.SHA256 != m.BinSHA256 {
		t.Errorf("bin hashes don't match a fresh hashFile run")
	}
}
```

- [ ] **Step 9: Update `inspect_test.go` — v0.2→v0.3 rejection coverage**

There's an existing test `TestCLIInspectRejectsV1` that builds a hand-crafted v0.1 container and asserts the rejection error. Either rename to `TestCLIInspectRejectsV2` or add a sibling test. The mechanical change: the test currently writes a `0x01` version byte and asserts the v0.1→v0.2 message; update to write a `0x02` version byte and assert the v0.2→v0.3 message.

Read `/home/hugh/miniscram/inspect_test.go` to find the existing test (around line 301), then update its version byte and asserted message string. Specifically:
- Change the byte written from `0x01` to `0x02`.
- Update the asserted substring from "v0.1" to "v0.2" (or whatever the existing assertion checks for).

If the existing test asserts a specific substring like "v0.1 .miniscram files cannot be read", change to "v0.2 .miniscram files cannot be read".

- [ ] **Step 10: Run focused tests**

Run: `go test ./... -run "TestPack|TestUnpack|TestVerify|TestCLIInspect" -v`
Expected: all PASS, including the new `TestPackPopulatesAllSixHashFields` and `TestVerifyDetectsScramHashMismatchAllThree` (with three subtests).

- [ ] **Step 11: Run full suite + vet**

Run: `go test ./... && go vet ./...`
Expected: all green.

- [ ] **Step 12: Build smoke check**

Run: `go build -o /tmp/miniscram-c1-smoke ./... && rm /tmp/miniscram-c1-smoke`
Expected: builds.

- [ ] **Step 13: Commit**

```bash
git add manifest.go pack.go unpack.go verify.go main.go pack_test.go unpack_test.go verify_test.go inspect_test.go
git commit -m "$(cat <<'EOF'
manifest: add md5+sha1 hashes; bump container format to v0.3

Manifest gains bin_md5, bin_sha1, scram_md5, scram_sha1 alongside
the existing sha256 fields. All three are computed in a single
I/O pass per file via hashFile (Task 1's helper). Pack populates
all six fields. Unpack and Verify recompute and check all three
on both bin and recovered scram via compareHashes; any single
hash mismatch is a hard failure (exit 5 for bin, exit 3 for output).

Sentinels renamed: errBinSHA256Mismatch → errBinHashMismatch,
errOutputSHA256Mismatch → errOutputHashMismatch. errVerifyMismatch
unchanged. Container version byte bumps 0x02 → 0x03 in lockstep
with FormatVersion 2 → 3. v0.2 containers are rejected with the
same migration-error pattern v0.1 used; per user there are no
extant v0.2 containers to preserve.

sha256File is removed; all callers migrated to hashFile.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Inspect human-format display of all six hashes

**Goal:** Add four `fmt.Fprintf` lines to `formatHumanInspect` so the new md5/sha1 fields display alongside the existing sha256 lines. JSON output passes through automatically. Tests confirm both human and JSON include all six hashes.

**Files:**
- Modify: `inspect.go`
- Modify: `inspect_test.go`

**Acceptance Criteria:**
- [ ] Human inspect output contains six lines: `bin_md5:`, `bin_sha1:`, `bin_sha256:`, `scram_md5:`, `scram_sha1:`, `scram_sha256:`, each with full lowercase hex.
- [ ] JSON inspect output's top-level keys include `bin_md5`, `bin_sha1`, `scram_md5`, `scram_sha1` (plus the existing `_sha256`).
- [ ] Existing inspect tests still pass.
- [ ] `go test ./...` PASS, `go vet ./...` clean.

**Verify:** `go test ./... -run TestInspect -v && go vet ./...`

**Steps:**

- [ ] **Step 1: Update `formatHumanInspect` in `inspect.go`**

In `/home/hugh/miniscram/inspect.go`, find the manifest-formatting block (around line 27–39). The current block prints `scram_size` then `scram_sha256` then `bin_size` then `bin_sha256`. Update to include md5/sha1 lines:

Replace:

```go
	fmt.Fprintf(&b, "  scram_size:             %d\n", m.ScramSize)
	fmt.Fprintf(&b, "  scram_sha256:           %s\n", m.ScramSHA256)
	fmt.Fprintf(&b, "  bin_size:               %d\n", m.BinSize)
	fmt.Fprintf(&b, "  bin_sha256:             %s\n", m.BinSHA256)
```

(Note: the actual existing code may have these in a slightly different order around line 31–34; the current declaration order is `bin_size`/`bin_sha256` before `scram_size`/`scram_sha256` in the formatter. Whichever order is present, preserve the same per-file grouping and add the md5/sha1 lines in the appropriate spot.)

with:

```go
	fmt.Fprintf(&b, "  scram_size:             %d\n", m.ScramSize)
	fmt.Fprintf(&b, "  scram_md5:              %s\n", m.ScramMD5)
	fmt.Fprintf(&b, "  scram_sha1:             %s\n", m.ScramSHA1)
	fmt.Fprintf(&b, "  scram_sha256:           %s\n", m.ScramSHA256)
	fmt.Fprintf(&b, "  bin_size:               %d\n", m.BinSize)
	fmt.Fprintf(&b, "  bin_md5:                %s\n", m.BinMD5)
	fmt.Fprintf(&b, "  bin_sha1:               %s\n", m.BinSHA1)
	fmt.Fprintf(&b, "  bin_sha256:             %s\n", m.BinSHA256)
```

(Column alignment: pad the new field-name colons to 23 columns to match the longest existing label `scram_sha256:` which is 13 chars + 10 spaces = 23. The Fprintf strings use `"%-23s"` style spacing — match that visually so the values line up.)

The actual existing column padding uses literal whitespace, not format-string padding. Match it: count the spaces between the colon and the start of the `%s` placeholder in the existing `scram_sha256:           %s` (10 spaces for sha256, more for md5/sha1 since they're shorter labels). Adjust to keep all values left-aligned at the same column.

- [ ] **Step 2: Add `TestInspectShowsAllSixHashes` to `inspect_test.go`**

Append:

```go
func TestInspectShowsAllSixHashes(t *testing.T) {
	path := packSyntheticContainer(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"inspect", path}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"bin_md5:",
		"bin_sha1:",
		"bin_sha256:",
		"scram_md5:",
		"scram_sha1:",
		"scram_sha256:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in inspect output", want)
		}
	}
}

func TestInspectJSONIncludesAllSixHashes(t *testing.T) {
	path := packSyntheticContainer(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"inspect", "--json", path}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d; stderr=%s", code, stderr.String())
	}
	var top map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &top); err != nil {
		t.Fatalf("JSON parse: %v", err)
	}
	for _, key := range []string{
		"bin_md5", "bin_sha1", "bin_sha256",
		"scram_md5", "scram_sha1", "scram_sha256",
	} {
		if v, ok := top[key]; !ok || v == "" {
			t.Errorf("JSON output missing or empty key %q (got %v)", key, v)
		}
	}
}
```

- [ ] **Step 3: Run focused tests**

Run: `go test ./... -run TestInspect -v`
Expected: all inspect tests pass, including the two new ones.

- [ ] **Step 4: Run full suite + vet**

Run: `go test ./... && go vet ./...`
Expected: all green.

- [ ] **Step 5: Manual smoke**

Run:
```bash
go build -o /tmp/miniscram-c1-final ./... && \
  cd /tmp && /tmp/miniscram-c1-final inspect /home/hugh/miniscram/freelancer/FL_v1.miniscram 2>&1 | grep -E "(md5|sha1|sha256)" || echo "no FL container yet — that's fine"
rm /tmp/miniscram-c1-final
```

(If you don't have a freshly-packed FL container handy, this step just confirms the build. The synthetic test in Step 3 provides the actual coverage.)

- [ ] **Step 6: Commit**

```bash
git add inspect.go inspect_test.go
git commit -m "$(cat <<'EOF'
inspect: display md5+sha1 hashes alongside sha256 in human format

Human-format inspect output now lists six hash lines (bin_md5,
bin_sha1, bin_sha256, scram_md5, scram_sha1, scram_sha256) in
the same per-file weakest-to-strongest grouping the manifest uses.

JSON output includes the new fields automatically because
formatJSONInspect re-marshals the manifest verbatim.

Closes TASKS.md C1.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review Notes

- **Spec coverage:** All five spec acceptance criteria map to tasks. (1) "manifest gains bin_md5, bin_sha1" → Task 2 Step 1. (2) "scram_md5, scram_sha1, scram_sha256" → Task 2 Step 1. (3) "all three computed in a single pass" → Task 1 (`hashFile` via `io.MultiWriter`). (4) "inspect shows all three" → Task 3. (5) "at unpack, all three are verified" → Task 2 Steps 3 (Unpack), 4 (Verify), 6/8 (per-hash tampering tests). The B-policy any-of-three exit-code semantics → Task 2 Step 5 (`main.go` switches), exercised by per-hash tampering matrix tests. The v0.2 rejection → Task 2 Steps 1, 9.
- **Placeholder scan:** No TBDs/TODOs. The "(may have these in a slightly different order around line 31–34...)" hedge in Task 3 Step 1 is a deliberate hedge against the implementer finding the existing field-print order slightly different from the spec's expected layout — instruction is to preserve per-file grouping, not to assert exact line order.
- **Type consistency:** `FileHashes` defined in Task 1 Step 3, used in Tasks 2 (pack/unpack/verify) and 3 (no — JSON passthrough doesn't need it). `hashFile`/`compareHashes` signatures consistent across tasks. Sentinels renamed once (Task 2 Step 3) and referenced consistently (Steps 5 main.go, 6 verify_test.go).
- **No external symbol dependencies introduced:** `crypto/md5` and `crypto/sha1` are stdlib. All other helpers used are already in the package.
- **Recovery from first-run surprises:** if the per-hash tampering tests reveal a sentinel mismatch (e.g., a code path returning errOutputHashMismatch where it should return errBinHashMismatch), the test failure makes it obvious. Fix at Task 2 commit time.
