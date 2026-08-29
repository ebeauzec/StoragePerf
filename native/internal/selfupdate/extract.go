package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// extract unpacks archivePath (a .tar.gz or .zip, matched by assetName's
// extension) into dest, stripping the archive's own single top-level
// directory (plumb-<version>-<platform>/...) the same way
// `tar --strip-components=1` does for run.sh, so dest ends up containing
// plumb/plumb.exe, config/, sidecars/ directly.
func extract(archivePath, assetName, dest string) error {
	if strings.HasSuffix(assetName, ".zip") {
		return extractZip(archivePath, dest)
	}
	return extractTarGz(archivePath, dest)
}

func stripTop(name string) (string, bool) {
	name = filepath.ToSlash(name)
	i := strings.IndexByte(name, '/')
	if i < 0 {
		return "", false // the top-level entry itself, not a file within it
	}
	return name[i+1:], true
}

func extractTarGz(archivePath, dest string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
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
		rel, ok := stripTop(hdr.Name)
		if !ok || rel == "" {
			continue
		}
		target, err := safeJoin(dest, rel)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		case tar.TypeSymlink:
			// The distributed archives don't contain symlinks (see
			// build-native.sh), but skip rather than fail outright if a
			// future change introduces one — nothing here depends on them.
		}
	}
}

func extractZip(archivePath, dest string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, zf := range zr.File {
		rel, ok := stripTop(zf.Name)
		if !ok || rel == "" {
			continue
		}
		target, err := safeJoin(dest, rel)
		if err != nil {
			return err
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, zf.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		rc.Close()
		out.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}

// safeJoin joins dest and rel, refusing to escape dest — defense against a
// malicious or corrupted archive containing "../" path traversal entries.
func safeJoin(dest, rel string) (string, error) {
	target := filepath.Join(dest, rel)
	if !strings.HasPrefix(target, filepath.Clean(dest)+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes destination", rel)
	}
	return target, nil
}
