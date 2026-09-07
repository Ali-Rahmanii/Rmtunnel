package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func genToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// disguiseAnswer is what the wizard collects for one [[disguise]] entry
// before it's rendered to TOML.
type disguiseAnswer struct {
	Type     string
	Addr     string // listen_addr (server) or server_addr (client)
	Domain   string
	Path     string
	CertFile string
	KeyFile  string
	Insecure bool
}

func wizardServer() {
	sectionHeader("ساخت تانل ایران (سرور)")

	fmt.Println(dim("این ویزارد کانفیگ سرور رو می‌سازه. پورت‌هایی که اینجا باز می‌کنی"))
	fmt.Println(dim("همون پورت‌هاییه که کاربر نهایی بهشون وصل می‌شه."))
	fmt.Println()

	token := readLineDefault("توکن امنیتی (خالی = خودکار بساز)", "")
	if token == "" {
		token = genToken()
		fmt.Println(green("توکن ساخته شد: ") + bold(token))
	}
	fmt.Println(yellow("⚠ این توکن رو دقیقاً همینطور توی کانفیگ کلاینت (سرور خارج) هم بذار."))
	fmt.Println()

	mode := "tcpmux"
	if !confirm("مود تونل روی tcpmux (مالتی‌پلکس، پیشنهادی) باشه؟", true) {
		mode = "tcp"
	}
	fmt.Println()

	disguises := askDisguises(true)
	fmt.Println()

	var ports []PortMap
	fmt.Println(bold("پورت‌هایی که می‌خوای فوروارد بشن رو وارد کن."))
	fmt.Println(dim("فرمت: پورت_عمومی=آدرس_مقصد_روی_سرور_خارج  (مثلا 8080=127.0.0.1:8080)"))
	fmt.Println(dim("یک خط خالی برای پایان دادن."))
	for {
		line := readLine(fmt.Sprintf("پورت %d (خالی=پایان): ", len(ports)+1))
		if line == "" {
			if len(ports) == 0 {
				fmt.Println(red("حداقل یک پورت لازمه."))
				continue
			}
			break
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			fmt.Println(red("فرمت اشتباهه. مثال: 8080=127.0.0.1:8080"))
			continue
		}
		ports = append(ports, PortMap{
			Listen: "0.0.0.0:" + strings.TrimSpace(parts[0]),
			Target: strings.TrimSpace(parts[1]),
		})
	}
	fmt.Println()

	tier := askTierPreset()

	toml := renderServerTOML(token, mode, disguises, ports, tier)
	finishWizard("server", toml, "rmtunnel-server")
}

func wizardClient() {
	sectionHeader("ساخت تانل خارج (کلاینت)")

	fmt.Println(dim("این باکس همونیه که بک‌اند واقعی (X-UI، پنل، وایرگارد و...) روشه یا بهش دسترسی داره."))
	fmt.Println()

	token := readLineDefault("توکن امنیتی (باید دقیقاً با سرور یکی باشه)", "")
	for token == "" {
		fmt.Println(red("توکن نمی‌تونه خالی باشه."))
		token = readLineDefault("توکن امنیتی", "")
	}
	fmt.Println()

	mode := "tcpmux"
	if !confirm("مود tcpmux (باید دقیقاً با سرور یکی باشه) باشه؟", true) {
		mode = "tcp"
	}
	fmt.Println()

	disguises := askDisguises(false)
	fmt.Println()

	tier := askTierPreset()

	toml := renderClientTOML(token, mode, disguises, tier)
	finishWizard("client", toml, "rmtunnel-client")
}

// askDisguises walks through the three disguise types, asking which are
// enabled and, for wss, its extra fields. isServer decides whether it asks
// for listen_addr or server_addr.
func askDisguises(isServer bool) []disguiseAnswer {
	fmt.Println(bold("کدوم روش‌های رد شدن از فیلترینگ فعال باشن؟"))
	fmt.Println(dim("پیشنهاد: هر سه‌تا رو فعال کن — کلاینت خودش بین اینا سوییچ می‌کنه."))
	fmt.Println(dim("توضیح کامل هرکدوم: docs/CENSORSHIP.md"))
	fmt.Println()

	var out []disguiseAnswer

	if confirm("  wss (TLS+WebSocket، شبیه یه سایت HTTPS معمولی — قوی‌ترین)", true) {
		d := disguiseAnswer{Type: "wss", Path: "/ws"}
		if isServer {
			port := readLineDefault("    پورت گوش دادن wss", "443")
			d.Addr = "0.0.0.0:" + port
			d.Domain = readLineDefault("    دامنه (اگه گواهی واقعی داری وارد کن، وگرنه خالی بذار)", "")
			d.CertFile = readLineDefault("    مسیر فایل cert (خالی = خودامضا)", "")
			if d.CertFile != "" {
				d.KeyFile = readLineDefault("    مسیر فایل key", "")
			}
		} else {
			ip := readLine("    آدرس عمومی سرور ایران: ")
			port := readLineDefault("    پورت wss سرور", "443")
			d.Addr = ip + ":" + port
			d.Domain = readLineDefault("    دامنه (دقیقاً همونی که سمت سرور زدی، یا خالی)", "")
			d.Insecure = confirm("    سرور از گواهی خودامضا استفاده می‌کنه؟", true)
		}
		out = append(out, d)
	}

	if confirm("  noise (رمزنگاری‌شده، بدون امضای پروتکل مشخص)", true) {
		d := disguiseAnswer{Type: "noise"}
		if isServer {
			port := readLineDefault("    پورت گوش دادن noise", "9001")
			d.Addr = "0.0.0.0:" + port
		} else {
			ip := readLine("    آدرس عمومی سرور ایران: ")
			port := readLineDefault("    پورت noise سرور", "9001")
			d.Addr = ip + ":" + port
		}
		out = append(out, d)
	}

	if confirm("  plain (خام، سریع‌ترین ولی راحت‌تر شناسایی میشه)", true) {
		d := disguiseAnswer{Type: "plain"}
		if isServer {
			port := readLineDefault("    پورت گوش دادن plain", "9000")
			d.Addr = "0.0.0.0:" + port
		} else {
			ip := readLine("    آدرس عمومی سرور ایران: ")
			port := readLineDefault("    پورت plain سرور", "9000")
			d.Addr = ip + ":" + port
		}
		out = append(out, d)
	}

	for len(out) == 0 {
		fmt.Println(red("حداقل یکی رو باید فعال کنی."))
		out = askDisguises(isServer)
	}
	return out
}

func askTierPreset() tierPreset {
	fmt.Println(bold("سطح عملکرد (اگه نمی‌دونی چی انتخاب کنی، «مبتدی» ok برای سایز سرور خارج از حد وحشیانه نیست):"))
	fmt.Println(menuItem("1", tiers[0].name))
	fmt.Println(menuItem("2", tiers[1].name+" (پیش‌فرض)"))
	fmt.Println(menuItem("3", tiers[2].name))
	fmt.Println(menuItem("4", tiers[3].name))
	fmt.Println(dim("یا اگه دقیق می‌خوای بدونی، بعد از ساخت کانفیگ از «بنچمارک» توی منو استفاده کن."))
	switch readLineDefault("انتخاب", "2") {
	case "1":
		return tiers[0]
	case "3":
		return tiers[2]
	case "4":
		return tiers[3]
	default:
		return tiers[1]
	}
}

func renderDisguiseBlock(d disguiseAnswer, isServer bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[[disguise]]\n")
	fmt.Fprintf(&b, "type = %q\n", d.Type)
	fmt.Fprintf(&b, "enabled = true\n")
	if isServer {
		fmt.Fprintf(&b, "listen_addr = %q\n", d.Addr)
	} else {
		fmt.Fprintf(&b, "server_addr = %q\n", d.Addr)
	}
	if d.Type == "wss" {
		fmt.Fprintf(&b, "domain = %q\n", d.Domain)
		fmt.Fprintf(&b, "path = %q\n", "/ws")
		if isServer {
			fmt.Fprintf(&b, "cert_file = %q\n", d.CertFile)
			fmt.Fprintf(&b, "key_file = %q\n", d.KeyFile)
		} else {
			fmt.Fprintf(&b, "insecure_skip_verify = %v\n", d.Insecure)
		}
	}
	return b.String()
}

func renderServerTOML(token, mode string, disguises []disguiseAnswer, ports []PortMap, tier tierPreset) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# ساخته‌شده توسط ویزارد rmtunnel — %s\n\n", RepoURL)
	fmt.Fprintf(&b, "mode = %q\ntoken = %q\n\n", mode, token)
	for _, d := range disguises {
		b.WriteString(renderDisguiseBlock(d, true))
		b.WriteString("\n")
	}
	for _, p := range ports {
		fmt.Fprintf(&b, "[[ports]]\nlisten = %q\ntarget = %q\n\n", p.Listen, p.Target)
	}
	b.WriteString(renderTuningBlock(tier))
	return b.String()
}

func renderClientTOML(token, mode string, disguises []disguiseAnswer, tier tierPreset) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# ساخته‌شده توسط ویزارد rmtunnel — %s\n\n", RepoURL)
	fmt.Fprintf(&b, "mode = %q\ntoken = %q\n\n", mode, token)
	for _, d := range disguises {
		b.WriteString(renderDisguiseBlock(d, false))
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "retry_min = \"1s\"\nretry_max = \"30s\"\n")
	b.WriteString(renderTuningBlock(tier))
	return b.String()
}

func renderTuningBlock(t tierPreset) string {
	return fmt.Sprintf(`heartbeat = "5s"
nodelay = true
keepalive = "15s"
recv_buf = %d
send_buf = %d
dial_timeout = "8s"
buffer_size = %d
mss = 0
reuse_port = false

min_idle = %d
max_idle = %d
idle_grace = "20s"

max_streams_per_session = %d
mux_frame_size = %d
mux_recv_buffer = %d
mux_stream_buffer = %d
mux_keepalive = "10s"
`, t.recvBuf, t.sendBuf, t.bufferSize, t.minIdle, t.maxIdle, t.maxStreamsPerSession, t.muxFrameSize, t.muxRecvBuffer, t.muxStreamBuffer)
}

// finishWizard shows the generated config, saves it, and offers to install
// it as a systemd service. role is "server" or "client"; unit is the
// matching systemd unit name.
func finishWizard(role, tomlText, unit string) {
	fmt.Println(bold(green("--- کانفیگ ساخته شد ---")))
	fmt.Println(dim(tomlText))

	defaultPath := defaultConfigPath(role)
	path := readLineDefault("مسیر ذخیره", defaultPath)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Println(red("خطا در ساخت پوشه: " + err.Error()))
		pressEnter()
		return
	}
	if err := os.WriteFile(path, []byte(tomlText), 0o600); err != nil {
		fmt.Println(red("خطا در ذخیره‌ی فایل: " + err.Error()))
		pressEnter()
		return
	}
	fmt.Println(green("ذخیره شد: " + path))

	if runtime.GOOS != "linux" {
		fmt.Println(dim("نصب سرویس systemd فقط روی لینوکس در دسترسه — کانفیگ رو به سرور واقعی منتقل کن."))
		pressEnter()
		return
	}

	if confirm("همین الان به‌عنوان سرویس systemd نصب و اجرا بشه؟", true) {
		installService(unit, role, path)
	}
	pressEnter()
}

func defaultConfigPath(role string) string {
	if runtime.GOOS == "linux" {
		return "/etc/rmtunnel/" + role + ".toml"
	}
	wd, _ := os.Getwd()
	return filepath.Join(wd, role+".toml")
}
