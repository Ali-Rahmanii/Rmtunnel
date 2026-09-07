package main

// Installing and running the real paqet binary — download, network
// auto-detection (the three things its README walks a user through finding
// by hand: interface, local IP, gateway MAC), and the iptables NOTRACK
// rules its own docs say are required on the listening side.

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// paqetArchAssetSuffix mirrors this project's own archAssetSuffix, but
// paqet's release workflow (build-release.yml) names its Linux asset
// "amd64"/"arm64" the same way, so the two happen to match.
func paqetArchAssetSuffix() string {
	if runtime.GOARCH == "arm64" {
		return "arm64"
	}
	return "amd64"
}

// paqetInstalled reports whether the binary is already in place, so the
// wizard can skip the download and get straight to the config questions on
// a re-run.
func paqetInstalled() bool {
	_, err := os.Stat(paqetBinPath)
	return err == nil
}

// installPaqetBinary downloads the latest paqet release for this box's
// architecture and installs it. paqet's asset filenames carry the release
// version (paqet-linux-<arch>-<version>.tar.gz — unlike this project's own,
// version-less asset names), so the exact URL has to come from the GitHub
// API rather than a fixed "latest/download" path.
func installPaqetBinary() error {
	rel, err := fetchLatestReleaseFrom(paqetRepoOwner, paqetRepoName, 15*time.Second)
	if err != nil {
		return fmt.Errorf("checking paqet releases: %w", err)
	}
	prefix := fmt.Sprintf("paqet-linux-%s-", paqetArchAssetSuffix())
	var assetURL string
	for _, a := range rel.Assets {
		if strings.HasPrefix(a.Name, prefix) && strings.HasSuffix(a.Name, ".tar.gz") {
			assetURL = a.BrowserDownloadURL
			break
		}
	}
	if assetURL == "" {
		return fmt.Errorf("no linux/%s asset found in paqet release %s", paqetArchAssetSuffix(), rel.TagName)
	}

	resp, err := http.Get(assetURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading paqet: http %s", resp.Status)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("paqet release asset isn't gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	binName := "paqet_linux_" + paqetArchAssetSuffix()
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s not found inside the release archive", binName)
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg || !strings.HasSuffix(hdr.Name, binName) {
			continue
		}
		out, err := os.OpenFile(paqetBinPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, tr)
		out.Close()
		if copyErr != nil {
			os.Remove(paqetBinPath)
			return copyErr
		}
		return nil
	}
}

// --- network auto-detection ----------------------------------------------

// detectedNetwork is what paqet's README walks a user through finding by
// hand via ip/arp — auto-filled here where possible, always left editable.
type detectedNetwork struct {
	Interface string
	LocalIP   string
	RouterMAC string
}

var (
	reDefaultRouteDev = regexp.MustCompile(`\bdev\s+(\S+)`)
	reDefaultRouteVia = regexp.MustCompile(`\bvia\s+(\S+)`)
	reSrcAddr         = regexp.MustCompile(`\bsrc\s+(\S+)`)
	reNeighMAC        = regexp.MustCompile(`(?:lladdr\s+|at\s+)([0-9a-fA-F:]{17})`)
)

// detectNetwork shells out to the same commands paqet's own README tells a
// user to run by hand (`ip route`, `ip neigh`/`arp`) — best-effort only:
// any failure just leaves the corresponding field blank for manual entry.
func detectNetwork() detectedNetwork {
	var d detectedNetwork
	if runtime.GOOS != "linux" {
		return d
	}

	route, err := run("ip", "route", "get", "1.1.1.1")
	if err != nil {
		return d
	}
	if m := reDefaultRouteDev.FindStringSubmatch(route); m != nil {
		d.Interface = m[1]
	}
	if m := reSrcAddr.FindStringSubmatch(route); m != nil {
		d.LocalIP = m[1]
	}

	gatewayIP := ""
	if m := reDefaultRouteVia.FindStringSubmatch(route); m != nil {
		gatewayIP = m[1]
	} else if def, err := run("ip", "route", "show", "default"); err == nil {
		if m := reDefaultRouteVia.FindStringSubmatch(def); m != nil {
			gatewayIP = m[1]
		}
	}
	if gatewayIP == "" {
		return d
	}

	if neigh, err := run("ip", "neigh", "show", gatewayIP); err == nil {
		if m := reNeighMAC.FindStringSubmatch(neigh); m != nil {
			d.RouterMAC = strings.ToLower(m[1])
		}
	}
	if d.RouterMAC == "" {
		if a, err := run("arp", "-n", gatewayIP); err == nil {
			if m := reNeighMAC.FindStringSubmatch(a); m != nil {
				d.RouterMAC = strings.ToLower(m[1])
			}
		}
	}
	return d
}

// applyPaqetIPTables applies the NOTRACK/RST-drop rules paqet's own README
// says are required on the listening side, or the whole tunnel is prone to
// random resets and drops under load — exactly the kind of "still not
// stable under real traffic" symptom this round is about. port is the
// paqet listen.addr port.
func applyPaqetIPTables(port string) error {
	rules := [][]string{
		{"-t", "raw", "-A", "PREROUTING", "-p", "tcp", "--dport", port, "-j", "NOTRACK"},
		{"-t", "raw", "-A", "OUTPUT", "-p", "tcp", "--sport", port, "-j", "NOTRACK"},
		{"-t", "mangle", "-A", "OUTPUT", "-p", "tcp", "--sport", port, "--tcp-flags", "RST", "RST", "-j", "DROP"},
	}
	for _, args := range rules {
		if _, err := exec.Command("iptables", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("iptables %s: %w", strings.Join(args, " "), err)
		}
	}
	return nil
}

// persistPaqetIPTables best-effort saves the rules just added so they
// survive a reboot — whichever save mechanism this distro actually has.
func persistPaqetIPTables() {
	if _, err := exec.LookPath("netfilter-persistent"); err == nil {
		run("netfilter-persistent", "save")
		return
	}
	if _, err := exec.LookPath("iptables-save"); err == nil {
		os.MkdirAll("/etc/iptables", 0o755)
		if out, err := exec.Command("iptables-save").Output(); err == nil {
			os.WriteFile("/etc/iptables/rules.v4", out, 0o644)
		}
	}
}
