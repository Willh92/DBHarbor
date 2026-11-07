package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOracleCharsetOverlayRoundTripAllCharsets(t *testing.T) {
	origPath, err := locateGoOraConvertersFile()
	if err != nil {
		t.Fatalf("locateGoOraConvertersFile: %v", err)
	}
	specs, err := buildAllSpecs(origPath)
	if err != nil {
		t.Fatalf("buildAllSpecs: %v", err)
	}
	if len(specs) < 230 {
		t.Fatalf("expected >= 230 Oracle charsets, got %d", len(specs))
	}

	gzData, err := packConverterSpecs(specs)
	if err != nil {
		t.Fatalf("packConverterSpecs: %v", err)
	}
	if len(gzData) > 550*1024 {
		t.Fatalf("expected compressed Oracle table < 550 KB, got %d bytes", len(gzData))
	}

	for _, orig := range specs {
		id := orig.Cases[0]
		got := unpackConverterSpec(gzData, id)
		if got == nil {
			t.Fatalf("unpackConverterSpec(%d) returned nil", id)
		}
		if got.FixedLangID != orig.FixedLangID ||
			got.CharWidth != orig.CharWidth ||
			got.EReplace != orig.EReplace ||
			got.DReplace != orig.DReplace {
			t.Fatalf("langID %d header mismatch: got %+v, want %+v", id, got, orig)
		}
		if !reflect.DeepEqual(got.DBuffer, orig.DBuffer) {
			t.Fatalf("langID %d DBuffer mismatch (len %d vs %d)", id, len(got.DBuffer), len(orig.DBuffer))
		}
		if !reflect.DeepEqual(got.DBuffer2, orig.DBuffer2) {
			t.Fatalf("langID %d DBuffer2 mismatch (len %d vs %d)", id, len(got.DBuffer2), len(orig.DBuffer2))
		}
		if !reflect.DeepEqual(got.EBuffer, orig.EBuffer) {
			t.Fatalf("langID %d EBuffer mismatch (len %d vs %d)", id, len(got.EBuffer), len(orig.EBuffer))
		}
		if !reflect.DeepEqual(got.Leading, orig.Leading) {
			t.Fatalf("langID %d Leading mismatch", id)
		}
	}

	if unpackConverterSpec(gzData, 999999) != nil {
		t.Fatalf("expected unknown langID 999999 to return nil")
	}

	// Stage slim go-ora overlay module and verify sync.Once single decompression + unknown langID non-pollution of convCache
	stagedOraDir := filepath.Join(t.TempDir(), "go-ora-v2")
	oraModRoot := filepath.Dir(filepath.Dir(origPath))
	if err := stageSlimGoOraModule(oraModRoot, stagedOraDir, []byte(renderOverlayGoSource(gzData))); err != nil {
		t.Fatalf("stageSlimGoOraModule: %v", err)
	}

	verifyTestSrc := `package converters

import "testing"

func TestGeneratedOverlaySyncOnceAndNoNilCachePollution(t *testing.T) {
	// Lookup 500 unknown langIDs: must return nil and NEVER pollute convCache!
	for badID := 900000; badID < 900500; badID++ {
		if got := NewStringConverter(badID); got != nil {
			t.Fatalf("expected nil converter for unknown langID %d", badID)
		}
		if c, err := loadConverterFromPacked(badID); err == nil || c != nil {
			t.Fatalf("expected unsupported charset error for unknown langID %d, got conv=%v err=%v", badID, c, err)
		}
	}
	convCacheMu.RLock()
	cacheLenAfterUnknown := len(convCache)
	convCacheMu.RUnlock()
	if cacheLenAfterUnknown != 0 {
		t.Fatalf("expected convCache len == 0 after 500 unknown langIDs, got %d", cacheLenAfterUnknown)
	}
	if len(rawArchive) == 0 || len(offsets) < 230 {
		t.Fatalf("expected sync.Once archive index initialized, got rawArchive=%d offsets=%d", len(rawArchive), len(offsets))
	}
	firstPtr := &rawArchive[0]

	// Valid Oracle WE8MSWIN1252 (178) & AL32UTF8 / ZHS16GBK (852) lookups succeed and reuse exact rawArchive backing slice
	for _, validID := range []int{1, 31, 178, 852, 871, 873} {
		if _, has := offsets[validID]; !has {
			continue
		}
		conv := NewStringConverter(validID)
		if conv == nil {
			t.Fatalf("expected non-nil converter for valid langID %d", validID)
		}
	}
	if &rawArchive[0] != firstPtr {
		t.Fatal("expected rawArchive slice to be decompressed once via sync.Once")
	}
}
`
	if err := os.WriteFile(filepath.Join(stagedOraDir, "converters", "overlay_cache_verify_test.go"), []byte(verifyTestSrc), 0o600); err != nil {
		t.Fatalf("write overlay_cache_verify_test.go: %v", err)
	}
	cmd := exec.Command(resolveGoBin(), "test", "-count=1", "-run", "TestGeneratedOverlaySyncOnceAndNoNilCachePollution", "./converters")
	cmd.Dir = stagedOraDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("staged overlay go test failed: %v\n%s", err, string(out))
	}
}
