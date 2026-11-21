package runner

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func writeBundle(t *testing.T, entries []struct {
	name, body, link string
	mode             int64
	dir              bool
}) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "b.tar.gz")
	fh, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(fh)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: e.mode, Linkname: e.link}
		switch {
		case e.dir:
			hdr.Typeflag = tar.TypeDir
		case e.link != "":
			hdr.Typeflag = tar.TypeSymlink
		default:
			hdr.Typeflag = tar.TypeReg
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fh.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUntarGzExtractsTree(t *testing.T) {
	bundle := writeBundle(t, []struct {
		name, body, link string
		mode             int64
		dir              bool
	}{
		{name: "sub", dir: true},
		{name: "hello.txt", body: "hi", mode: 0o644},
		{name: "sub/run.sh", body: "#!/bin/sh\n", mode: 0o755},
	})
	dest := t.TempDir()
	if err := untarGz(bundle, dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "hello.txt"))
	if err != nil || string(data) != "hi" {
		t.Fatalf("hello.txt: %v %q", err, data)
	}
	info, err := os.Stat(filepath.Join(dest, "sub", "run.sh"))
	if err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("run.sh: %v mode %v", err, info.Mode())
	}
}

func TestUntarGzSkipsTraversal(t *testing.T) {
	dest := t.TempDir()
	bundle := writeBundle(t, []struct {
		name, body, link string
		mode             int64
		dir              bool
	}{
		{name: "../evil.txt", body: "x", mode: 0o644},
		{name: "a/../../evil2.txt", body: "x", mode: 0o644},
		{name: "/abs.txt", body: "x", mode: 0o644},
		{name: "ok.txt", body: "y", mode: 0o644},
	})
	if err := untarGz(bundle, dest); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../evil.txt", "evil2.txt", "abs.txt"} {
		if _, err := os.Lstat(filepath.Join(dest, bad)); !os.IsNotExist(err) {
			t.Fatalf("traversal entry %q was written", bad)
		}
	}
	if _, err := os.Lstat(filepath.Join(dest, "ok.txt")); err != nil {
		t.Fatal("legitimate file missing after traversal entries")
	}
}

func TestUntarGzSkipsEscapingSymlink(t *testing.T) {
	bundle := writeBundle(t, []struct {
		name, body, link string
		mode             int64
		dir              bool
	}{
		{name: "ok", link: "target.txt"},
		{name: "target.txt", body: "y", mode: 0o644},
		{name: "bad", link: "../../etc/passwd"},
		{name: "abs", link: "/etc/passwd"},
	})
	dest := t.TempDir()
	if err := untarGz(bundle, dest); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"bad", "abs"} {
		if _, err := os.Lstat(filepath.Join(dest, bad)); !os.IsNotExist(err) {
			t.Fatalf("escaping symlink %q was created", bad)
		}
	}
	if _, err := os.Lstat(filepath.Join(dest, "ok")); err != nil {
		t.Fatal("in-workspace symlink missing")
	}
}

func TestUntarAutoBothCodecs(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "x.txt"), []byte("data"), 0o644)
	for _, codec := range []string{"gzip", "zstd"} {
		out := filepath.Join(root, "a-"+codec)
		if err := tarCompressed([]artifactEntry{{file: filepath.Join(src, "x.txt"), root: src}}, out, codec); err != nil {
			t.Fatalf("%s pack: %v", codec, err)
		}
		dest := filepath.Join(root, "d-"+codec)
		os.MkdirAll(dest, 0o755)
		if err := untarAuto(out, dest); err != nil {
			t.Fatalf("%s unpack: %v", codec, err)
		}
		b, err := os.ReadFile(filepath.Join(dest, "x.txt"))
		if err != nil || string(b) != "data" {
			t.Fatalf("%s roundtrip: %q %v", codec, b, err)
		}
	}
}
