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
	sectionHeader("آپدیت اسکریپت")
	fmt.Println("نسخه‌ی فعلی: " + bold(Version))
	fmt.Println(dim("در حال بررسی " + RepoURL + " ..."))

	rel, err := fetchLatestRelease()
	if err != nil {
		fmt.Println(red("خطا در گرفتن اطلاعات نسخه: " + err.Error()))
		pressEnter()
		return
	}
	fmt.Println("آخرین نسخه‌ی منتشرشده: " + bold(rel.TagName))

	if rel.TagName == "v"+Version || rel.TagName == Version {
		fmt.Println(green("از قبل روی آخرین نسخه‌ای."))
		pressEnter()
		return
	}

	if runtime.GOOS != "linux" {
		fmt.Println(dim("آپدیت خودکار فقط برای باینری‌های منتشرشده‌ی لینوکسیه."))
		fmt.Println(dim("روی این سیستم از سورس بیلد بگیر: git pull && go build -o rmtunnel ."))
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
		fmt.Println(red("فایل مناسب معماری این سیستم (" + assetName + ") توی release پیدا نشد."))
		pressEnter()
		return
	}

	if !confirm("دانلود و جایگزینی نسخه‌ی جاری با "+rel.TagName+"؟", true) {
		return
	}
	if err := downloadAndReplaceSelf(assetURL); err != nil {
		fmt.Println(red("آپدیت ناموفق بود: " + err.Error()))
		pressEnter()
		return
	}
	fmt.Println(green("آپدیت شد به " + rel.TagName + ". اجرای بعدی برنامه از نسخه‌ی جدید استفاده می‌کنه."))
	pressEnter()
}

func menuBenchInteractive() {
	sectionHeader("بنچمارک سرعت و سخت‌افزار")
	fmt.Println(dim("این تست باید روی هر دو سرور (ایران و خارج) اجرا بشه: یکی server یکی client."))
	fmt.Println()
	fmt.Println(menuItem("1", "این سیستم منتظر بمونه (سمت server بنچ)"))
	fmt.Println(menuItem("2", "این سیستم به یه سرور بنچ وصل بشه و تست بگیره (سمت client بنچ)"))
	fmt.Println(menuItem("0", "برگشت"))

	switch readLine("انتخاب: ") {
	case "1":
		addr := readLineDefault("آدرس گوش دادن", "0.0.0.0:9999")
		token := readLineDefault("توکن موقت (فقط برای این تست)", genToken())
		fmt.Println(dim("منتظر اتصال... (Ctrl+C برای توقف)"))
		if err := runBenchServer(addr, token); err != nil {
			fmt.Println(red(err.Error()))
			pressEnter()
		}
	case "2":
		addr := readLine("آدرس سرور بنچ (ip:port): ")
		token := readLine("توکن: ")
		if err := runBenchClient(addr, token); err != nil {
			fmt.Println(red(err.Error()))
		}
		pressEnter()
	}
}
