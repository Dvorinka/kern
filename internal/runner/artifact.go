package runner

// actions/upload-artifact and actions/download-artifact, executed natively on the
// runner host: the workspace lives outside the container, so globbing and tar
// happen here. Uploads stream to the control plane, which owns blob storage.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

func (e *executor) artifactUpload(step StepSpec) (string, int) {
	name := withString(step, "name", "artifact")
	paths := withLines(step, "path")
	if len(paths) == 0 {
		return "failure", 1
	}
	ws := filepath.Join(e.workRoot(), "workspace")
	// GitHub stores files relative to each search path's literal root
	// (dist/** -> version.txt, not dist/version.txt).
	var files []artifactEntry
	for _, p := range paths {
		literal := p
		if i := strings.IndexAny(p, "*?["); i >= 0 {
			literal = p[:i]
		}
		root := filepath.Clean(literal)
		if info, err := os.Stat(filepath.Join(ws, literal)); err == nil && info.IsDir() {
			root = filepath.Clean(literal)
		} else if i := strings.LastIndex(literal, "/"); i >= 0 {
			root = filepath.Clean(literal[:i])
		} else {
			root = "."
		}
		matches, err := doublestar.FilepathGlob(filepath.Join(ws, p))
		if err != nil {
			e.logStep(step, "KERN: invalid artifact pattern "+p)
			return "failure", 1
		}
		for _, m := range matches {
			files = append(files, artifactEntry{file: m, root: filepath.Join(ws, root)})
		}
	}
	if len(files) == 0 {
		e.logStep(step, "KERN: no files match artifact path(s); upload skipped")
		return "success", 0 // GitHub v4 defaults to error only when if-no-files-found=error
	}
	if w := withString(step, "if-no-files-found", ""); w == "error" && len(files) == 0 {
		return "failure", 1
	}
	tmp := filepath.Join(e.workRoot(), name+".tar.gz")
	if err := tarGz(files, tmp); err != nil {
		e.logStep(step, "KERN: pack artifact: "+err.Error())
		return "failure", 1
	}
	defer os.Remove(tmp)
	if err := e.c.uploadArtifact(context.Background(), e.jobID, name, tmp); err != nil {
		e.logStep(step, "KERN: upload artifact: "+err.Error())
		return "failure", 1
	}
	e.logStep(step, fmt.Sprintf("KERN: uploaded artifact %q (%d files)", name, len(files)))
	return "success", 0
}

func (e *executor) artifactDownload(step StepSpec) (string, int) {
	name := withString(step, "name", "")
	dest := withString(step, "path", ".")
	ws := filepath.Join(e.workRoot(), "workspace")
	target := filepath.Join(ws, filepath.Clean(dest))
	if strings.HasPrefix(filepath.Clean(dest), "..") {
		return "failure", 1
	}
	names := []string{}
	if name != "" {
		names = []string{name}
	} else {
		list, err := e.c.listArtifacts(context.Background(), e.jobID)
		if err != nil || len(list) == 0 {
			e.logStep(step, "KERN: no artifacts in this run to download")
			return "success", 0
		}
		names = list
	}
	for _, n := range names {
		tmp := filepath.Join(e.workRoot(), "dl-"+n+".tar.gz")
		if err := e.c.downloadArtifact(context.Background(), e.jobID, n, tmp); err != nil {
			e.logStep(step, "KERN: download artifact "+n+": "+err.Error())
			return "failure", 1
		}
		dir := target
		if name == "" {
			dir = filepath.Join(target, n) // GitHub: unnamed download extracts each into <name>/
		}
		if err := untarGz(tmp, dir); err != nil {
			e.logStep(step, "KERN: extract artifact: "+err.Error())
			return "failure", 1
		}
		os.Remove(tmp)
	}
	e.logStep(step, fmt.Sprintf("KERN: downloaded %d artifact(s) into %s", len(names), dest))
	return "success", 0
}

func (e *executor) logStep(step StepSpec, msg string) {
	_ = e.c.appendLog(context.Background(), e.jobID, step.Number, []byte(msg+"\n"))
}

func withString(s StepSpec, key, def string) string {
	if v, ok := s.With[key]; ok {
		if str, ok := v.(string); ok && strings.TrimSpace(str) != "" {
			return str
		}
	}
	return def
}

func withLines(s StepSpec, key string) []string {
	v, ok := s.With[key]
	if !ok {
		return nil
	}
	var out []string
	add := func(str string) {
		for _, line := range strings.Split(str, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				out = append(out, line)
			}
		}
	}
	switch t := v.(type) {
	case string:
		add(t)
	case []any:
		for _, item := range t {
			if str, ok := item.(string); ok {
				add(str)
			}
		}
	}
	return out
}

type artifactEntry struct{ file, root string }

func tarGz(files []artifactEntry, out string) error {
	fh, err := os.Create(out)
	if err != nil {
		return err
	}
	defer fh.Close()
	gw := gzip.NewWriter(fh)
	tw := tar.NewWriter(gw)
	for _, f := range files {
		info, err := os.Stat(f.file)
		if err != nil || info.IsDir() {
			continue
		}
		rel, err := filepath.Rel(f.root, f.file)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		src, err := os.Open(f.file)
		if err != nil {
			return err
		}
		if _, err := io.Copy(tw, src); err != nil {
			src.Close()
			return err
		}
		src.Close()
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gw.Close()
}

func untarGz(archive, dest string) error {
	fh, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer fh.Close()
	gz, err := gzip.NewReader(fh)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			continue // path traversal guard
		}
		path := filepath.Join(dest, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
}
