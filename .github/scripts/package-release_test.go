package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPackageManifestMatchesArchiveAndLibrary(t *testing.T) {
	tempDir := t.TempDir()
	libraryPath := filepath.Join(tempDir, "codex-switch-guard.dll")
	archivePath := filepath.Join(tempDir, "codex-switch-guard_1.2.3_windows_amd64.zip")
	libraryData := []byte("test shared library")
	if errWrite := os.WriteFile(libraryPath, libraryData, 0o755); errWrite != nil {
		t.Fatal(errWrite)
	}

	archiveData, errPackage := packageLibrary(libraryPath, archivePath)
	if errPackage != nil {
		t.Fatal(errPackage)
	}
	manifest, errManifest := buildArtifactManifest(libraryPath, archivePath, archiveData, "1.2.3", "windows", "amd64", "commit-sha")
	if errManifest != nil {
		t.Fatal(errManifest)
	}
	if manifest.SchemaVersion != 1 || manifest.Version != "1.2.3" || manifest.GOOS != "windows" || manifest.GOARCH != "amd64" || manifest.SourceCommit != "commit-sha" {
		t.Fatalf("manifest metadata = %#v", manifest)
	}
	if manifest.LibrarySize != int64(len(libraryData)) || manifest.Archive == "" || manifest.ArchiveSHA256 == "" || manifest.LibrarySHA256 == "" {
		t.Fatalf("manifest hashes = %#v", manifest)
	}

	reader, errOpen := zip.NewReader(bytes.NewReader(archiveData), int64(len(archiveData)))
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	if len(reader.File) != 1 || reader.File[0].Name != manifest.Library {
		t.Fatalf("archive entries = %#v", reader.File)
	}
	entry, errEntry := reader.File[0].Open()
	if errEntry != nil {
		t.Fatal(errEntry)
	}
	packagedLibrary, errRead := io.ReadAll(entry)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if errClose := entry.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	if !bytes.Equal(packagedLibrary, libraryData) {
		t.Fatalf("packaged library = %q", packagedLibrary)
	}
	archiveSum := sha256.Sum256(archiveData)
	librarySum := sha256.Sum256(packagedLibrary)
	if manifest.ArchiveSHA256 != hex.EncodeToString(archiveSum[:]) || manifest.LibrarySHA256 != hex.EncodeToString(librarySum[:]) {
		t.Fatalf("manifest hashes = %#v", manifest)
	}
}
