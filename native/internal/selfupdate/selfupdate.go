// Package selfupdate implements Plumb's one exception to the
// check-and-notify-only promise the rest of internal/updates makes: when a
// newer Plumb release exists, a user can click "Update now" and have this
// package fetch it, preserve the running install's database and real
// config, launch the new version, and hand off to it — all without a
// terminal.
//
// Safety properties, by construction:
//   - The currently-running install directory is never modified or deleted.
//     The new version is extracted into a sibling directory, so a failed or
//     unwanted update is trivially reversible: stop the new process and
//     start the old one again from where it already sits.
//   - data/ (the VictoriaMetrics database, Prometheus buffer, logs,
//     reports) and the two files a user actually edits — config/arrays.yml
//     and config/settings.yml — are copied into the new install before
//     it's ever started. Everything else (the binary, sidecars,
//     config/thresholds/*.yml) comes from the new release, matching what
//     run.sh/run.ps1 already do for a manual re-run.
//   - What this does NOT do: verify a cryptographic signature on the
//     download. There is no code-signing infrastructure for this project
//     to check against, so this relies on the same trust boundary as
//     run.sh/run.ps1 already do — HTTPS to api.github.com and
//     objects.githubusercontent.com, and that this GitHub repo itself
//     hasn't been compromised. Worth knowing before relying on this at an
//     air-gapped or high-security site — PLUMB_CHECK_FOR_UPDATES=false
//     disables this capability (and all update checking) entirely.
package selfupdate

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const Repo = "ebeauzec/StoragePerf"

// Platform returns the os_arch string build-native.sh uses for release
// asset names (e.g. "darwin_arm64"), or an error for a combination no
// release is published for.
func Platform() (string, error) {
	os_ := runtime.GOOS
	arch := runtime.GOARCH
	switch {
	case os_ == "windows" && arch == "amd64":
		return "windows_amd64", nil
	case (os_ == "darwin" || os_ == "linux") && (arch == "amd64" || arch == "arm64"):
		return os_ + "_" + arch, nil
	default:
		return "", fmt.Errorf("no published release for %s/%s", os_, arch)
	}
}

type Release struct {
	Tag       string
	AssetURL  string
	AssetName string
}

// FetchLatest finds the latest GitHub release's asset for this platform.
// Fetches the full release object (not just the tag) since the asset
// filename embeds the version and there's no fixed name to guess —
// exactly why run.sh greps for it instead of constructing it.
func FetchLatest(client *http.Client) (Release, error) {
	platform, err := Platform()
	if err != nil {
		return Release{}, err
	}
	req, err := http.NewRequest("GET", "https://api.github.com/repos/"+Repo+"/releases/latest", nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Release{}, fmt.Errorf("github releases/latest: %s", resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Release{}, err
	}
	ext := ".tar.gz"
	if platform == "windows_amd64" {
		ext = ".zip"
	}
	suffix := "-" + platform + ext
	for _, a := range body.Assets {
		if len(a.Name) > len(suffix) && a.Name[len(a.Name)-len(suffix):] == suffix {
			return Release{Tag: body.TagName, AssetURL: a.BrowserDownloadURL, AssetName: a.Name}, nil
		}
	}
	return Release{}, fmt.Errorf("no %s asset found in release %s", platform, body.TagName)
}

// download fetches url to a new temp file and returns its path. Caller
// removes it.
func download(client *http.Client, url string) (string, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download: %s", resp.Status)
	}
	f, err := os.CreateTemp("", "plumb-update-*")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// Apply downloads rel, extracts it into a new sibling directory of
// currentRoot, copies over data/ and the user's real arrays.yml/
// settings.yml from currentRoot, and returns the new install's root path.
// currentRoot itself is never modified.
func Apply(client *http.Client, currentRoot string, rel Release) (newRoot string, err error) {
	archivePath, err := download(client, rel.AssetURL)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", rel.AssetName, err)
	}
	defer os.Remove(archivePath)

	// Sibling of currentRoot, named after the release — human-recognizable,
	// and guaranteed not to collide with the directory still running.
	newRoot = filepath.Join(filepath.Dir(currentRoot), "plumb-"+rel.Tag+"-update")
	if err := os.RemoveAll(newRoot); err != nil {
		return "", fmt.Errorf("clearing %s: %w", newRoot, err)
	}
	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		return "", err
	}

	if err := extract(archivePath, rel.AssetName, newRoot); err != nil {
		os.RemoveAll(newRoot)
		return "", fmt.Errorf("extracting %s: %w", rel.AssetName, err)
	}

	exePath := filepath.Join(newRoot, exeName())
	if info, err := os.Stat(exePath); err != nil || info.Size() == 0 {
		os.RemoveAll(newRoot)
		return "", fmt.Errorf("extracted archive has no usable %s", exeName())
	}

	if err := preserveUserState(currentRoot, newRoot); err != nil {
		os.RemoveAll(newRoot)
		return "", fmt.Errorf("carrying over data/config: %w", err)
	}

	return newRoot, nil
}

func exeName() string {
	if runtime.GOOS == "windows" {
		return "plumb.exe"
	}
	return "plumb"
}

// preserveUserState copies (not moves — currentRoot must be left untouched
// and still runnable) data/, config/arrays.yml, and config/settings.yml
// from the running install into the newly extracted one, overwriting
// whatever the fresh archive shipped in their place. Mirrors run.sh's
// preserve/restore, adapted to "copy" since the old install stays live
// throughout this call.
func preserveUserState(currentRoot, newRoot string) error {
	oldData := filepath.Join(currentRoot, "data")
	if info, err := os.Stat(oldData); err == nil && info.IsDir() {
		if err := copyTree(oldData, filepath.Join(newRoot, "data")); err != nil {
			return err
		}
	}
	for _, f := range []string{"arrays.yml", "settings.yml"} {
		src := filepath.Join(currentRoot, "config", f)
		if _, err := os.Stat(src); err != nil {
			continue // never configured, or a mock-only install — nothing to carry over
		}
		if err := copyFile(src, filepath.Join(newRoot, "config", f)); err != nil {
			return err
		}
	}
	return nil
}

// StartAndHandoff spawns the new install's executable as a fully detached
// process (survives this one exiting) and returns once it's launched.
// Doesn't wait for it to be healthy — it binds the same port this process
// is still holding, so it can't succeed until the caller shuts this
// process down; see main.go's bounded listen-retry on the other side of
// that handoff, and the browser-side polling that confirms the swap
// actually completed.
func StartAndHandoff(newRoot string) error {
	logPath := filepath.Join(newRoot, "data", "logs")
	if err := os.MkdirAll(logPath, 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(logPath, "plumb.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	fmt.Fprintf(logFile, "\n--- relaunched via self-update at %s ---\n", time.Now().Format(time.RFC3339))

	return spawnDetached(filepath.Join(newRoot, exeName()), newRoot, logFile)
}
