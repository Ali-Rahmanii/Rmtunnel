# نقشه‌ی دستکاری و بهینه‌سازی

فهرست هر چیزی که می‌شه روی این تونل دستکاری کرد، از ساده (فقط عوض کردن یک
عدد در toml) تا پیشرفته. هر مورد اشاره می‌کنه دقیقاً کجای کد رو باز کنی.

برای ضد-فیلترینگ و منطق سوییچ خودکار بین transportها، `docs/CENSORSHIP.md`
رو ببین — این فایل فقط پرفورمنسه.

---

## قدم اول: بذار خودش بگه چی لازم داری

قبل از دستی تنظیم کردن هر چیزی:

```bash
rmtunnel bench server 0.0.0.0:9999 <token>      # روی یکی از سرورها
rmtunnel bench client <آدرس-سرور>:9999 <token>   # روی سرور دیگه
```

این RTT و throughput واقعی مسیر رو اندازه می‌گیره، هسته/RAM محلی رو می‌خونه
(RAM فقط روی لینوکس — حتماً روی سرور واقعی اجرا کن نه ویندوز خودت)، و یک
سطح پیشنهادی (light/medium/heavy/insane) با مقادیر آماده برای paste کردن توی
toml چاپ می‌کنه. جدول زیر رو برای فهم *چرا* هر مقدار مهمه بخون، نه برای حدس
زدن مقدارش.

**راه ساده‌تر:** ویزارد منو (گزینه‌ی ۱ یا ۲) خودش می‌تونه همین بنچمارک رو
مستقیم اجرا کنه و مقادیر رو خودکار توی کانفیگ بذاره — نیازی به کپی دستی
نیست.

### یه باگ واقعی که همینجا پیدا و رفع شد

نسخه‌ی اولیه‌ی `wss` سرعتش خیلی کمتر از پهنای‌باند واقعی اندازه‌گیری‌شده بود.
دلیلش: `recv_buf`/`send_buf`/`nodelay` روی همه‌ی disguiseها اعمال می‌شد
**به‌جز wss** — چون gorilla/websocket خودش کانکشن TCP خام رو می‌سازه و هیچ
hook‌ای برای تیون کردنش نبود. با `NetDialContext` سمت کلاینت و wrap کردن
listener سمت سرور رفع شد (`wss.go`). همچنین `mux_stream_buffer` (سقف
جریان هر stream توی tcpmux) قبلاً بر اساس BDP تنظیم نمی‌شد و می‌تونست
throughput یک اتصال تکی رو محدود کنه؛ الان `bench.go`'s `tunedTier.resolved()`
این رو هم مثل `recv_buf`/`send_buf` بر اساس BDP بالا می‌بره.

---

## سطح ۱ — فقط کانفیگ (بدون تغییر کد)

| پارامتر | فایل | اثر |
|---|---|---|
| `min_idle` / `max_idle` | config.go | تعداد اتصال/سشن آماده‌به‌کار |
| `idle_grace` | config.go | چقدر طول بکشه تا مازاد `max_idle` جمع بشه |
| `buffer_size` | config.go, pipe.go | بافر کپی هر جهت |
| `nodelay` | config.go, dial.go | خاموش کردنش throughput بهتر، لتنسی بدتر |
| `recv_buf` / `send_buf` | config.go, dial.go | باید طبق BDP (bandwidth × RTT) تنظیم بشه — `rmtunnel bench` این رو خودکار حساب می‌کنه |
| `max_streams_per_session` | config.go | فقط mux — چند اتصال هم‌زمان روی هر session |
| `mss` | config.go, mss_linux.go | **فقط لینوکس.** کلمپ MSS — وقتی تونل خودش داخل یه تونل/VPN دیگه با MTU کوچیک اجرا می‌شه لازمه |
| `reuse_port` | config.go, reuseport_linux.go | **فقط لینوکس.** SO_REUSEPORT برای بار سنگین اتصال هم‌زمان |
| `heartbeat` | config.go | فاصله‌ی ping/RTT — کوتاه‌تر یعنی تشخیص سریع‌تر قطعی، ترافیک کنترلی بیشتر (ناچیز) |

---

## سطح ۲ — نکات فنی که از قبل حل شدن (نیازی به کاری نیست)

### Zero-copy روی TCP خام (فعال، مجانی)
`io.CopyBuffer` در `pipe.go`، وقتی هر دو طرف `*net.TCPConn` باشن، خودکار از
`sendfile`/`splice` سطح کرنل استفاده می‌کنه (Go 1.11+). در مود `tcp` کاملاً
فعاله؛ در `tcpmux` فقط نیم‌مسیر سمت بک‌اند محلی (که `*net.TCPConn`ه) این رو
داره — سمت smux.Stream یک لایه‌ی کاربریه و نمی‌تونه.

### MSS Clamp و SO_REUSEPORT (پیاده‌سازی شده، فقط باید فعال‌شون کنی)
هر دو با build-tag فقط روی لینوکس فعالن (`mss_linux.go`,
`reuseport_linux.go`) و روی بقیه‌ی سیستم‌عامل‌ها no-op هستن — پس بیلد ویندوز
نمی‌شکنه. با `mss` و `reuse_port` در toml فعالشون کن.

### Noise encryption (پیاده‌سازی شده)
`[[disguise]] type = "noise"` — رمزنگاری NNpsk0 با کلید مشتق‌شده از توکن.
جزئیات کامل و منطق طراحی در `noise.go` و `docs/CENSORSHIP.md`.

---

## سطح ۳ — تغییرات کد که می‌تونن اثر واقعی داشته باشن

### ۱. الگوریتم adaptive pool بر اساس throughput واقعی
الگوریتم فعلی در `client.go`'s `maintainer()` فقط به تعداد اتصال باز نگاه
می‌کنه (`min_idle`/`max_idle`/`idle_grace`)، نه throughput واقعی. برای رفتار
adaptive بر پایه‌ی Mbit/s واقعی، یک شمارنده‌ی bytes-per-tick اضافه کن (از
`TotalBytesTransferred()` در `pipe.go` بخون) و شرط رشد pool رو به «throughput
داره از X% ظرفیت فعلی رد می‌شه» گره بزن.

### ۲. TLS fingerprint واقعی (uTLS) برای disguise `wss`
الان `wss` از `crypto/tls` استاندارد Go استفاده می‌کنه که fingerprint خودش رو
داره (متفاوت از Chrome/Firefox واقعی). برای دفاع در برابر JA3/JA4
fingerprinting پیشرفته، باید `github.com/refraction-networking/utls` رو جای
`crypto/tls` سمت کلاینت (`wss.go`'s `wssDial`) بذاری. این دقیقاً همون کاری
که BackPack برای WSS خودش می‌کنه.

### ۳. رمزنگاری با padding برای مخفی کردن شکل ترافیک
`noise.go` فعلی encrypt می‌کنه ولی سایز رکوردها رو padding نمی‌کنه — یعنی
اندازه‌ی پکت‌ها همچنان یه pattern قابل‌مشاهده‌ست (حتی اگه محتوا رمزنگاری‌شده
باشه). اضافه کردن padding تصادفی *داخل* لایه‌ی رمزنگاری (نه به شکل واضح روی
سیم) این pattern رو محو می‌کنه. رجوع کن به کامنت‌های `noise.go` برای جزئیات
trade-off.

### ۴. فوروارد UDP — پیاده‌سازی شده
`ports = [{... , udp = true}]` — همون carrier (pool connection یا mux
stream) که TCP استفاده می‌کنه رو با target مارک‌شده به `udp:` قرض می‌گیره و
دیتاگرام‌ها رو با فریم طول-پیشوندی رد و بدل می‌کنه، نه یک ترنسپورت جدا. کد و
دلایل طراحی در `udp.go`.

### ۵. محدودیت پهنای‌باند/تعداد اتصال (rate limiting)
هرکسی که توکن رو داشته باشه بدون سقف مصرف می‌کنه. برای اضافه کردن، یک
`io.Reader`/`io.Writer` wrapper با `golang.org/x/time/rate` دور
`handleLocalConn` (server.go) و معادلش سمت کلاینت کافیه.

### ۶. تنظیمات سطح OS (بدون تغییر کد Go)
روی سرور لینوکسی، این‌ها مستقل از کد پروژه‌ن ولی معمولاً بیشترین اثر رو دارن:
```
# BBR — معمولاً بزرگ‌ترین بهبود throughput روی لینک‌های پرافت‌وخیز
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr

net.core.rmem_max = 26214400
net.core.wmem_max = 26214400
net.core.somaxconn = 4096
net.ipv4.tcp_max_syn_backlog = 4096
```

---

## پیشنهاد قدم بعدی

با `rmtunnel bench` مقادیر واقعی رو بگیر، `mss`/`reuse_port` رو روی سرور
لینوکسی فعال کن، BBR رو ست کن — این سه‌تا صفر خط کد جدید لازم دارن و مستقیم
روی throughput مسیر ایران↔خارج اثر می‌ذارن. بعدش اگه هنوز فیلتر می‌خوری،
سراغ uTLS (سطح ۳.۲) برو.
