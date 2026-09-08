package main

// Backup & Restore bundles every tunnel config this box knows about (both
// engines — rmtunnel's own .toml and paqet's .yaml, see tunnels.go's
// tunnelRoles) plus the sysctl tuning file and the auto-refresh schedule
// into one archive. Deliberately backs up configs only, not the generated
// systemd unit files: those bake in an absolute binary path and a specific
// box's paqet install, which restoring onto a *different* server (the
// actual point of a backup — moving to a new box, not just recovering the
// same one) would get wrong. Restore instead regenerates every unit fresh
// via the same installTunnelService/installPaqetService this box's own
// wizard uses, so it always matches wherever it's actually running.

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const backupsDir = "/etc/rmtunnel/backups"

// backupManifest entries are the absolute paths bundled into an archive —
// listed explicitly (not just "everything under tunnelsRoot") so restore
// knows exactly what it's writing back and where, the same "no surprises"
// approach askPorts and the wizard's own config rendering already take.
func backupManifest() []string {
	var paths []string
	for _, t := range listTunnels() {
		paths = append(paths, t.Path)
	}
	if _, err := os.Stat(sysctlConfPath); err == nil {
		paths = append(paths, sysctlConfPath)
	}
	if _, err := os.Stat(autoRefreshTimerPath); err == nil {
		paths = append(paths, autoRefreshTimerPath)
	}
	return paths
}

func menuBackupRestore() {
	for {
		sectionHeader("Backup & Restore")
		fmt.Println(dim("A backup bundles every tunnel config on this box, the sysctl tuning"))
		fmt.Println(dim("file, and the auto-refresh schedule into one file."))
		fmt.Println(dim("Backups live in " + backupsDir))
		fmt.Println()
		fmt.Println(menuItem("1", "Create a backup file"))
		fmt.Println(menuItem("2", "Restore from a backup file"))
		fmt.Println(menuItem("0", "back"))

		switch askChoiceNoCancel("choice") {
		case "1":
			createBackup()
		case "2":
			restoreBackup()
		default:
			return
		}
	}
}

// askChoiceNoCancel is readLine for this screen's own menu, deliberately
// not askChoice — "0" here means "back to the Manage rmtunnel menu," not
// "cancel the whole wizard," so it must not panic via wizardCancelled.
func askChoiceNoCancel(prompt string) string {
	return strings.TrimSpace(readLine(prompt + ": "))
}

func createBackup() {
	fmt.Println()
	if runtime.GOOS != "linux" {
		fmt.Println(dim("this needs the real tunnel/config paths — only available on Linux."))
		pressEnter()
		return
	}

	paths := backupManifest()
	if len(paths) == 0 {
		fmt.Println(dim("nothing to back up yet — no tunnels configured."))
		pressEnter()
		return
	}

	if err := os.MkdirAll(backupsDir, 0o755); err != nil {
		fmt.Println(red("failed to create " + backupsDir + ": " + err.Error()))
		pressEnter()
		return
	}

	name := "rmtunnel-backup-" + time.Now().Format("2006-01-02-150405") + ".tar.gz"
	archivePath := filepath.Join(backupsDir, name)

	if err := writeBackupArchive(archivePath, paths); err != nil {
		fmt.Println(red("backup failed: " + err.Error()))
		pressEnter()
		return
	}

	fmt.Println(green("backup created: " + archivePath))
	fmt.Printf("  (%d file(s) bundled)\n", len(paths))
	fmt.Println()
	fmt.Println(bold("To move this to a new server, run this FROM the new server:"))
	fmt.Println(dim("  scp root@<this-box-ip>:" + archivePath + " /root/"))
	fmt.Println(dim("then, on the new server, once rmtunnel is installed there:"))
	fmt.Println(dim("  rmtunnel menu   →   Manage rmtunnel → Backup & Restore → Restore from a backup file"))
	fmt.Println(dim("  path: /root/" + name))
	pressEnter()
}

// writeBackupArchive stores each file at its own absolute path inside the
// tar — so restore can write every entry straight back to Name with no
// path-remapping logic of its own, and the same archive is legible with
// plain `tar tzf` too.
func writeBackupArchive(archivePath string, paths []string) error {
	f, err := os.Create(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue // a file listed but gone since (race with a delete) — skip, not fatal
		}
		hdr := &tar.Header{
			Name: strings.TrimPrefix(p, "/"),
			Mode: 0o600,
			Size: int64(len(data)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	return nil
}

func restoreBackup() {
	fmt.Println()
	if runtime.GOOS != "linux" {
		fmt.Println(dim("this needs the real tunnel/config paths — only available on Linux."))
		pressEnter()
		return
	}

	entries, _ := os.ReadDir(backupsDir)
	var found []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".tar.gz") {
			found = append(found, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(found)))

	if len(found) > 0 {
		fmt.Println(bold("Backups found in " + backupsDir + ":"))
		for i, name := range found {
			fmt.Println(menuItem(fmt.Sprint(i+1), name))
		}
		fmt.Println(dim("or enter a full path to a backup file from elsewhere."))
		fmt.Println()
	}

	answer := askChoiceNoCancel("backup file (number above, or a path)")
	if answer == "" {
		return
	}
	archivePath := answer
	if idx := indexFromChoice(answer, len(found)); idx >= 0 {
		archivePath = filepath.Join(backupsDir, found[idx])
	}

	restored, err := extractBackupArchive(archivePath)
	if err != nil {
		fmt.Println(red("restore failed: " + err.Error()))
		pressEnter()
		return
	}
	if len(restored) == 0 {
		fmt.Println(red("that archive had nothing this box recognizes."))
		pressEnter()
		return
	}

	fmt.Println(green(fmt.Sprintf("restored %d file(s):", len(restored))))
	for _, p := range restored {
		fmt.Println("  " + p)
	}
	fmt.Println()

	if strings.Contains(strings.Join(restored, "\n"), sysctlConfPath) {
		if confirm("apply the restored sysctl tuning now?", true) {
			applySysctlTuning()
		}
	}

	for _, t := range listTunnels() {
		if !containsPath(restored, t.Path) {
			continue
		}
		if active, _ := serviceStatus(t.unit()); active {
			continue // already running (a restore onto the SAME box) — nothing to (re)install
		}
		if !confirm("install and start restored tunnel \""+t.Name+"\" ("+t.Role+") as a service now?", true) {
			continue
		}
		if t.isPaqet() {
			installPaqetService(t.unit(), t.Path)
		} else {
			installTunnelService(t.Role, t.Name, t.Path)
		}
	}
	pressEnter()
}

func containsPath(paths []string, p string) bool {
	for _, x := range paths {
		if x == p {
			return true
		}
	}
	return false
}

// extractBackupArchive writes every entry straight back to its own stored
// absolute path (see writeBackupArchive) and returns the list actually
// written — deliberately refusing anything that doesn't land under
// tunnelsRoot, the sysctl file, or the auto-refresh unit paths, so a
// tampered or unrelated archive can't be used to write outside those.
func extractBackupArchive(archivePath string) ([]string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("not a valid backup archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	var restored []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return restored, err
		}
		dest := "/" + hdr.Name
		if !allowedRestorePath(dest) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return restored, err
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return restored, err
		}
		if err := os.WriteFile(dest, data, 0o600); err != nil {
			return restored, err
		}
		restored = append(restored, dest)
	}
	return restored, nil
}

func allowedRestorePath(p string) bool {
	return strings.HasPrefix(p, tunnelsRoot+"/") || p == sysctlConfPath ||
		p == autoRefreshTimerPath || p == autoRefreshServicePath
}
