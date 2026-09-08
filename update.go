package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"sync/atomic"
	"time"
)

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func fetchLatestRelease(timeout time.Duration) (*ghRelease, error) {
	return fetchLatestReleaseFrom(RepoOwner, RepoName, timeout)
}

// fetchLatestReleaseFrom is the same lookup against any repo — used for
// this project's own updates and, separately, to find the current paqet
// release (see paqet_install.go), which unlike this project's own assets
// carries its version in the filename and so can't use a fixed
// "latest/download" URL.
func fetchLatestReleaseFrom(owner, repo string, timeout time.Duration) (*ghRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("github api %s: %s", resp.Status, string(body))
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func archAssetSuffix() string {
	if runtime.GOARCH == "arm64" {
		return "arm64"
	}
	return "amd64"
}

func downloadAndReplaceSelf(url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %s", resp.Status)
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}
	tmp := self + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, resp.Body)
	out.Close()
	if copyErr != nil {
		os.Remove(tmp)
		return copyErr
	}
	// Rename over a running executable works on Linux — the process keeps
	// its already-open inode, and the new file only takes effect on the
	// next launch. This would fail on Windows (the file stays locked while
	// running), which is fine: rmtunnel's release binaries are Linux-only.
	return os.Rename(tmp, self)
}

func menuUpdate() {
	sectionHeader("Update Script")
	fmt.Println("current version: " + bold(Version))
	fmt.Println(dim("checking " + RepoURL + " ..."))

	rel, err := fetchLatestRelease(10 * time.Second)
	if err != nil {
		fmt.Println(red("failed to fetch release info: " + err.Error()))
		pressEnter()
		return
	}
	fmt.Println("latest published version: " + bold(rel.TagName))

	if !isNewerVersion(rel.TagName) {
		fmt.Println(green("already on the latest version."))
		pressEnter()
		return
	}

	if runtime.GOOS != "linux" {
		fmt.Println(dim("automatic updates only work for the published Linux binaries."))
		fmt.Println(dim("on this system, build from source instead: git pull && go build -o rmtunnel ."))
		pressEnter()
		return
	}

	assetName := "rmtunnel-linux-" + archAssetSuffix()
	var assetURL string
	for _, a := range rel.Assets {
		if a.Name == assetName {
			assetURL = a.BrowserDownloadURL
		}
	}
	if assetURL == "" {
		fmt.Println(red("no asset for this system's architecture (" + assetName + ") found in the release."))
		pressEnter()
		return
	}

	if !confirm("download and replace the current version with "+rel.TagName+"?", true) {
		return
	}
	if err := downloadAndReplaceSelf(assetURL); err != nil {
		fmt.Println(red("update failed: " + err.Error()))
		pressEnter()
		return
	}
	fmt.Println(green("updated to " + rel.TagName + "."))

	// Replacing the binary on disk doesn't touch a systemd service that's
	// already running the old one in memory — Linux keeps the old inode
	// alive under the running process until something restarts it. Left
	// alone, that's exactly what happened before this existed: the menu
	// reported "updated", but the actual tunnel kept running the old build
	// indefinitely, silently, until someone thought to restart it by hand.
	migrated := migrateLegacyTunnels()
	for _, name := range migrated {
		fmt.Println(green("migrated legacy tunnel \"" + name + "\" to the multi-tunnel layout."))
	}

	tunnels := listTunnels()
	if len(tunnels) > 0 {
		fmt.Println(dim(fmt.Sprintf("restarting %d tunnel(s) so they run %s...", len(tunnels), rel.TagName)))
		for _, t := range tunnels {
			if _, err := run("systemctl", "restart", t.unit()); err != nil {
				fmt.Println(red("  failed to restart " + t.unit() + " — restart it by hand from \"Manage rmtunnel\"."))
			} else {
				fmt.Println(green("  restarted " + t.unit()))
			}
		}
	}
	pressEnter()
}

// legacyTunnel describes a pre-v0.3.0 install: a single flat config
// (/etc/rmtunnel/<role>.toml) and a non-templated unit (rmtunnel-<role>),
// from before tunnels got names and lived under /etc/rmtunnel/tunnels/. A
// box updated straight from that layout keeps that old service running
// under a unit name the new "Manage tunnels" menu never looks at — it isn't
// merely out of date, it's invisible, and a wizard-built tunnel on the same
// port then collides with it instead of replacing it.
type legacyTunnel struct {
	role       string
	unit       string
	unitFile   string
	configPath string
}

func legacyTunnels() []legacyTunnel {
	var out []legacyTunnel
	for _, role := range []string{"server", "client"} {
		unit := "rmtunnel-" + role
		out = append(out, legacyTunnel{
			role:       role,
			unit:       unit,
			unitFile:   "/etc/systemd/system/" + unit + ".service",
			configPath: "/etc/rmtunnel/" + role + ".toml",
		})
	}
	return out
}

// migrateLegacyTunnels moves any pre-v0.3.0 flat-layout tunnel it finds into
// the new named-tunnel layout, under the name "main" (or "legacy"/"legacyN"
// if that's already taken), installs it as the new templated unit, and
// retires the old one — so it shows up in "Manage tunnels" from now on
// instead of running invisibly in the background under a name nothing looks
// for anymore. Returns the names of whatever it migrated, role/name form.
func migrateLegacyTunnels() []string {
	var migrated []string
	for _, lt := range legacyTunnels() {
		if _, err := os.Stat(lt.unitFile); err != nil {
			continue // no legacy install of this role — nothing to do
		}
		data, err := os.ReadFile(lt.configPath)
		if err != nil {
			// Unit file exists but its config is already gone — disable the
			// orphaned unit and move on, there's nothing left to preserve.
			run("systemctl", "disable", "--now", lt.unit)
			os.Remove(lt.unitFile)
			continue
		}

		name := "main"
		if _, err := os.Stat(tunnelConfigPath(lt.role, name)); err == nil {
			for i := 1; ; i++ {
				cand := "legacy"
				if i > 1 {
					cand = fmt.Sprintf("legacy%d", i)
				}
				if _, err := os.Stat(tunnelConfigPath(lt.role, cand)); err != nil {
					name = cand
					break
				}
			}
		}

		if err := os.MkdirAll(tunnelDir(lt.role), 0o755); err != nil {
			fmt.Println(red("failed to migrate " + lt.configPath + ": " + err.Error()))
			continue
		}
		newPath := tunnelConfigPath(lt.role, name)
		if err := os.WriteFile(newPath, data, 0o600); err != nil {
			fmt.Println(red("failed to migrate " + lt.configPath + ": " + err.Error()))
			continue
		}

		run("systemctl", "disable", "--now", lt.unit)
		os.Remove(lt.unitFile)

		if _, err := LoadConfig(newPath, lt.role); err != nil {
			fmt.Println(yellow("⚠ migrated " + lt.configPath + " but it no longer validates (" + err.Error() + ") — fix it from \"Manage rmtunnel\" → Manage tunnel → Edit before starting it."))
			migrated = append(migrated, lt.role+"/"+name)
			continue
		}
		installTunnelService(lt.role, name, newPath)
		os.Remove(lt.configPath)
		migrated = append(migrated, lt.role+"/"+name)
	}
	return migrated
}

func isNewerVersion(tag string) bool {
	return tag != "v"+Version && tag != Version
}

// --- startup update check -------------------------------------------------
//
// Checked once, in the background, the moment the menu starts — not on
// every redraw, which would mean a GitHub API call every time a wizard or
// submenu returns to the main screen. The result (empty until the check
// finishes, or if it fails, or if this is already the latest version) is
// picked up by the next banner draw, so it typically appears by the time
// the user has read the main menu once.
var updateNotice atomic.Pointer[string]

func startUpdateCheck() {
	go func() {
		rel, err := fetchLatestRelease(4 * time.Second)
		if err != nil || !isNewerVersion(rel.TagName) {
			return
		}
		msg := fmt.Sprintf("A new version is available: %s (you're on v%s). Run \"Update script\" from the menu.", rel.TagName, Version)
		updateNotice.Store(&msg)
	}()
}

func currentUpdateNotice() string {
	p := updateNotice.Load()
	if p == nil {
		return ""
	}
	return *p
}

func menuBenchInteractive() {
	sectionHeader("Speed & Hardware Benchmark")
	fmt.Println(dim("run this on both boxes (Iran and Kharej): one as server, one as client."))
	fmt.Println()
	fmt.Println(menuItem("1", "this box waits (bench server side)"))
	fmt.Println(menuItem("2", "this box connects to a bench server and tests (bench client side)"))
	fmt.Println(menuItem("0", "back"))

	switch readLine("choice: ") {
	case "1":
		addr := readLineDefault("listen address", "0.0.0.0:9999")
		token := readLineDefault("temporary token (for this test only)", genToken())
		fmt.Println(dim("waiting for a connection... (Ctrl+C to stop)"))
		if err := runBenchServer(addr, token); err != nil {
			fmt.Println(red(err.Error()))
			pressEnter()
		}
	case "2":
		addr := readLine("bench server address (ip:port): ")
		token := readLine("token: ")
		if err := runBenchClient(addr, token); err != nil {
			fmt.Println(red(err.Error()))
		}
		pressEnter()
	}
}
