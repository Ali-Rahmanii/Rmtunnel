package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"time"
)

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func fetchLatestRelease() (*ghRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", RepoOwner, RepoName)
	client := &http.Client{Timeout: 10 * time.Second}
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

	rel, err := fetchLatestRelease()
	if err != nil {
		fmt.Println(red("failed to fetch release info: " + err.Error()))
		pressEnter()
		return
	}
	fmt.Println("latest published version: " + bold(rel.TagName))

	if rel.TagName == "v"+Version || rel.TagName == Version {
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
	fmt.Println(green("updated to " + rel.TagName + ". the next run will use the new version."))
	pressEnter()
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
