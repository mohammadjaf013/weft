# راهنمای کامل نصب و راه‌اندازی Weft
### برای ادمین‌ها — نسخه سرویس‌دهی (Deployment)

---

## ۱) Weft چیست؟

Weft یک **عامل پردازش رسانه** (Media Processing Agent) است. یک فایل صوتی یا
ویدیو می‌گیرد، آن را با `ffmpeg` پردازش می‌کند (تبدیل به HLS چند رندیشن،
ساخت thumbnail، استخراج/تولید زیرنویس، …) و خروجی آماده را **خودکار به
مقصد** (دیسک محلی، سرور SSH، یا S3) آپلود می‌کند.

مثال: یک `film.mp4` با فرمان زیر تبدیل و منتشر می‌شود:

```
film.mp4
   ↓  (یک‌بار decode)
   ├── playlist.m3u8        ← master playlist (ارجاع به همه رندیشن‌ها)
   ├── 360p.m3u8 + 360p_000.ts …   ← رندیشن 360p
   ├── 480p.m3u8 + 480p_000.ts …   ← رندیشن 480p
   ├── 720p.m3u8 + 720p_000.ts …   ← رندیشن 720p
   ├── 1080p.m3u8 + 1080p_000.ts … ← رندیشن 1080p
   ├── thumbnails/          ← poster + sprite + preview.vtt
   └── subtitle/<lang>/     ← زیرنویس per-language
```

**نکته کلیدی:** خروجی دقیقاً مطابق ساختار `admin-convert.sh` سابق است —
اگر آن اسکریپت را می‌شناسید، همین خروجی را می‌گیرید ولی با قابلیت‌های
صف‌بندی، اولویت‌بندی، webhook، و مقصدهای چندگانه.

---

## ۲) معماری در یک نگاه

```
┌─────────────────────────────────────────────────────────┐
│  CLI (weft.exe)  ←── HTTP/JSON ──→  REST API (chi)     │
│                        │                                │
│                        ▼                                │
│  DAG Scheduler ──→ Worker ها (تعداد قابل تنظیم)          │
│                        │                                │
│                        ▼                                │
│  پلاگین‌ها: hls, thumbnail, subtitle, ai-subtitle,      │
│            upload                                       │
│                        │                                │
│                        ▼                                │
│  Storage: local / ssh (key یا password) / s3/minio/r2   │
└─────────────────────────────────────────────────────────┘
```

- **دیتابیس**: یک فایل SQLite (`weft.db`) — بدون نیاز به سرویس خارجی.
- **هر عملیات** + رویدادش در **یک تراکنش** ذخیره می‌شود (Event Sourcing) —
  اگر وسط کار کرش کند، با انقضای lease کار دوباره برمی‌دارد.
- **وب‌هوک**: رویدادها (`job.completed`, `job.failed`, …) با امضای HMAC و
  تلاش مجدد به سرور شما فرستاده می‌شوند.

---

## ۳) پیش‌نیازها

| نیاز | نسخه | توضیح |
|---|---|---|
| ffmpeg | جدید (≥ 5) | در PATH سیستم (یا مسیرش در کانفیگ) |
| ffprobe | همراه ffmpeg | در PATH سیستم |
| سیستم | Windows / Linux | باینری تک‌فایله، بدون CGO |
| (اختیاری) Go | 1.24+ | فقط برای ساخت از سورس |

> اگر `ffmpeg`/`ffprobe` روی سیستم نباشد، `doctor` خطا می‌دهد و job ها
> فیل می‌شوند.

---

## ۴) نصب

### گزینه A — باینری آماده
فایل `weft.exe` را در یک پوشه (مثل `C:\weft`) کپی کنید.

### گزینه B — ساخت از سورس
```powershell
git clone <repo> weft
cd weft
go build ./...
go build -o weft.exe ./cmd/weft
```

### بررسی محیط
```powershell
.\weft.exe doctor
```
خروجی `0` یعنی سالم. هر خطای غیرصفر یعنی مشکل (ffmpeg نیست، کانفیگ خراب، …).

---

## ۵) پیکربندی (weft.yaml)

اولین بار با فرمان زیر ساخته می‌شود:

```powershell
.\weft.exe init-config
```

فایل `weft.yaml` تولید می‌شود. بخش‌های مهم:

### 5.1 شبکه و امنیت (مهمترین)
```yaml
network:
  listen: 127.0.0.1:8443   # ← اگر باید از بیرون در دسترس باشد: 0.0.0.0:8443
  tls: "off"                # ← "on" برای HTTPS (نیازمند certificate)

security:
  api_keys: true
  admin_api_key: "CHANGE-THIS-TO-STRONG-KEY"   # ← حتماً عوض شود!
  webhook_signing: hmac-sha256
```

> ⚠️ **حتماً `admin_api_key` را عوض کنید.** بدون کلید قوی، هر کس که به پورت
> دسترسی داشته باشد می‌تواند job بسازد و از دیسک/منابع استفاده کند.

### 5.2 ظرفیت و صف
```yaml
workers:
  min: 2                # چند job همزمان اجرا شود
  max: 0                # 0 = بدون سقف
  lease_ttl_seconds: 300

scheduler:
  max_cpu_percent: 85   # سقف مصرف CPU برای صف‌بندی داینامیک
```

**اولویت‌ها** (از بالا به پایین، فوری‌ترین اول):
`emergency` → `high` → `normal` → `low` → `background`

### 5.3 مقصد خروجی (پیش‌فرض)
```yaml
storage:
  local:
    base_path: ./data    # خروجی job ها اینجا ذخیره می‌شود
database:
  path: weft.db
```

### 5.4 پلاگین‌ها
```yaml
plugins:
  enabled:
    - hls
    - thumbnail
    - subtitle
    - upload
    - ai-subtitle        # فقط اگر می‌خواهی AI زیرنویس بسازد
    - storage-local
    - storage-ssh
    - storage-s3
```

### 5.5 زیرنویس AI (اختیاری)
```yaml
ai_subtitle:
  provider: whisper      # whisper | gemini | hybrid
  whisper:
    model_path: "C:\models\ggml-base.bin"   # فایل مدل whisper
    language: "en"        # زبانِ صدا (منبع)، مثلا en یا tr؛ به whisper -l داده می‌شود
    threads: 8            # -t تعداد هسته CPU برای whisper
    temperature: 0.0      # --temperature؛ 0.0 = خروجی قطعی/بهتر برای ترجمه
    prompt: "Spider-Man, Peter Parker, Zendaya, Green Goblin, Hulk, Scorpian"
                          # --prompt متن اولیه (اسم شخصیت‌ها/اصطلاحات)؛ کیفیت را خیلی بالا می‌برد
    bin_path: ""          # مسیر whisper-cli (پیش‌فرض whisper-cli در PATH)
  gemini:
    api_key: "..."        # یا کلید API گوگل (برای hybrid ترجمه/بهبود لازم است)
    model: "gemini-1.5-flash"
    language: "fa"        # زبان مقصد زیرنویس (پیش‌فرض)
  auto_generate:
    enabled: false
    target_languages: [fa, en]
```

**provider ها:**
- `whisper`: فقط رونویسی با مدل محلی (آفلاین). اگر `src_lang` با `lang` فرق کند، ترجمه نمی‌شود.
- `gemini`: رونویسی مستقیم با API (بدون whisper).
- `hybrid`: whisper رونویسی می‌کند → gemini **ترجمه** می‌کند (زمان‌بندی دست‌نخورده). وقتی `src_lang` مساوی `lang` باشد، فقط متن را اصلاح می‌کند.

**زبان منبع در برابر زبان مقصد:** در هر job با `weft jobs create --src-lang en --lang fa` می‌توانی به whisper بگویی صدا به انگلیسی است (منبع) و خروجی را به فارسی بخواهی؛ در حالت hybrid gemini ترجمه را انجام می‌دهد.

**نصب whisper.cpp (روی لینوکس):**
```bash
dnf install -y cmake gcc-c++ git       # یا apt install ...
git clone https://github.com/ggerganov/whisper.cpp /opt/whisper.cpp
cd /opt/whisper.cpp
cmake -B build --buildtype=Release
cmake --build build --config Release -j $(nproc)
cp build/bin/whisper-cli /usr/local/bin/
# دانلود مدل (medium ≈ 1.5GB — روی سرور 62GB RAM خیلی خوب است)
mkdir -p /opt/weft/models
curl -L -o /opt/weft/models/ggml-medium.bin \
  https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-medium.bin
```

**نکته مهم — فرمت ورودی:** whisper.cpp فقط صوتی خام (wav/pcm 16kHz) می‌خواند، نه mp4/mkv. Weft این کار را **خودکار** انجام می‌دهد: قبل از whisper با ffmpeg صدا را به `16kHz mono wav` استخراج می‌کند. اگر فیل شد (`whisper produced no srt`)، علتش معمولاً باینری whisper-cli قدیمی یا مسیر مدل اشتباه است — stderr واقعی whisper حالا در `error` job نمایش داده می‌شود.

---

## ۶) اجرای سرویس

```powershell
.\weft.exe serve --config weft.yaml
```

- یک دیمن در پس‌زمینه اجرا می‌شود (یا با Task Scheduler / systemd).
- همه فرمان‌های دیگر CLI به همین دیمن از طریق HTTP وصل می‌شوند.
- دیتابیس `weft.db` و پوشه `data/` در **دایرکتوری اجرای دیمن** ساخته می‌شوند.

**ویندوز (اجرای خودکار در پس‌زمینه):**
```powershell
Start-Process weft.exe -ArgumentList "serve","--config","weft.yaml" -WindowStyle Hidden
```
یا با **Task Scheduler** طوری تنظیم کنید که هنگام ورود / boot اجرا شود.

---

## ۷) دیپلوی روی سرور لینوکس

### 7.1 ساخت باینری لینوکس (از ویندوز)

```powershell
# آماده‌سازی متغیرهای محیطی
$env:GOOS="linux"; $env:GOARCH="amd64"; $env:CGO_ENABLED="0"

# برای amd64 (اکثر سرورها)
go build -ldflags "-s -w" -o dist/weft-linux-amd64 .\cmd\weft

# برای arm64 (مثلاً Raspberry Pi / ARM server)
$env:GOARCH="arm64"
go build -ldflags "-s -w" -o dist/weft-linux-arm64 .\cmd\weft
```

خروجی: `dist/weft-linux-amd64` و `dist/weft-linux-arm64` — باینری تک‌فایله، بدون CGO.

### 7.2 به‌روزرسانی روی سرور

> نکته: CLI از هر پوشه‌ای کانفیگ را پیدا می‌کند:
> `./weft.yaml` ← `/opt/weft/weft.yaml` ← `/etc/weft/weft.yaml` ← `~/.weft/weft.yaml`

```bash
# ۱) سرویس فعلی را بکش
pkill -f "weft.*serve"

# ۲) باینری جدید را جایگزین کن
cp /tmp/weft-linux-amd64 /usr/local/bin/weft
chmod +x /usr/local/bin/weft

# ۳) دوباره اجرا کن (از پوشه‌ی کانفیگ)
cd /opt/weft
nohup /usr/local/bin/weft serve --config /opt/weft/weft.yaml > /opt/weft/weft.log 2>&1 &

# ۴) چک کن بالا آمده
sleep 2
pgrep -af weft
tail -20 /opt/weft/weft.log
```

### 7.3 تست سریع بعد از دیپلوی

```bash
weft version
weft doctor

# سیستم (RAM/CPU/HDD)
weft system

# یک زیرنویس به فیلمِ منتشرشده اضافه کن
weft jobs create ./fa.srt --profile subtitle-add --lang fa --name movie --path Series-Test/movie1
weft jobs list

# master را ببین که SUBTITLES دارد
cat /opt/weft/data/Series-Test/movie1/movie_master.m3u8
```

---

## ۸) دستورهای روزمره (CLI)

### ساخت و مدیریت Job
```powershell
# ساخت job
.\weft.exe jobs create "D:\videos\film.mp4" --profile vod-h264
.\weft.exe jobs create "D:\videos\film.mp4" --profile vod-h264 --priority high
.\weft.exe jobs create "D:\videos\film.mp4" --profile vod-h264 --destination 1
.\weft.exe jobs create "D:\videos\film.mp4" --profile vod-h264 --lang fa

# مشاهده
.\weft.exe jobs list
.\weft.exe jobs list --status completed
.\weft.exe jobs get <job-id>
.\weft.exe jobs events <job-id>

# کنترل
.\weft.exe jobs action <job-id> cancel
.\weft.exe jobs action <job-id> retry
.\weft.exe jobs action <job-id> pause
.\weft.exe jobs action <job-id> resume
.\weft.exe jobs priority <job-id> emergency   # تغییر اولویت تا وقتی هنوز صف است
.\weft.exe jobs delete <job-id>               # حذف کامل (فقط برای job های تمام‌شده)
```

### پروفایل‌ها
| پروفایل | ورودی | خروجی |
|---|---|---|
| `vod-h264` | ویدیو | HLS ۴ رندیشن + thumbnail + زیرنویس + master + آپلود |
| `vod-hevc` | ویدیو | مثل بالا با کدک HEVC |
| `vod-encode` | ویدیو | مثل `vod-h264` بدون زیرنویس/AI زیرنویس: HLS ۴ رندیشن + thumbnail + master + آپلود |
| `audio` | صوتی | m4a + آپلود |
| `audio-hls` | صوتی | m4a + HLS صوتی (m3u8 + ts) + آپلود |
| `thumbnail` | ویدیو | poster + sprite + vtt + آپلود |
| `ai-subtitle` | ویدیو/صوتی | زیرنویس تولیدشده با AI + آپلود |
| `subtitle-add` | فایل SRT/VTT | افزودن/جایگزینی زیرنویس به ویدیوی قبلاً-منتشرشده + به‌روزرسانی master |
| `dub-add` | فایل صوتی | افزودن/جایگزینی دوبله به ویدیوی قبلاً-منتشرشده + به‌روزرسانی master |
| `trim-update` | ویدیوی اصلی | برش (trim) دوباره‌ی ویدیوی قبلاً-منتشرشده، جای‌گزین همان فایل‌ها |
| `poster-replace` | تصویر (jpg/png/webp) | جایگزینی پوستر ویدیوی قبلاً-منتشرشده (بدون ffmpeg) |

**افزودن/جایگزینی زیرنویس یا دوبله به ویدیوی از-قبل-منتشرشده** (بدون پردازش مجدد ویدیو):

```bash
# زیرنویس: --lang زبان، --name اسم پایه‌ی فیلم، --path پوشه‌ی همان فیلم
weft jobs create ./movie_fa.srt --profile subtitle-add --lang fa --name movie --path Series-Test/movie1

# دوبله (صوت فارسی جایگزین)
weft jobs create ./dub_fa.mp3 --profile dub-add --lang fa --name movie --path Series-Test/movie1
```

- خروجی زیرنویس: `subtitle/<lang>/<name>.vtt` + master با `EXT-X-MEDIA:TYPE=SUBTITLES`
- خروجی دوبله: `audio/<lang>/<name>.m3u8` + master با `EXT-X-MEDIA:TYPE=AUDIO`
- **دوباره با همان `--lang`** = جایگزینی (نسخه دوم ساخته نمی‌شود)
- **با `--lang` جدید** = ترک دوم کنار ترک قبلی
- `--name` را همیشه همان اسم پایه‌ی فیلم بده (`movie`) تا master به همان فایل اشاره کند

### مشاهده وضعیت سیستم
```powershell
.\weft.exe queue        # خلاصه صف بر اساس priority
.\weft.exe workers      # worker ها (busy/idle + task جاری)
.\weft.exe plugins      # پلاگین‌های فعال
.\weft.exe metrics      # خروجی Prometheus
.\weft.exe benchmark    # بنچمارک CPU/ffmpeg (یا benchmark get برای آخرین نتیجه)
.\weft.exe system       # وضعیت سرور: RAM/CPU/دیسک + uptime
.\weft.exe dashboard    # داشبورد زنده ترمینال: job/صف/worker/سیستم با کنترل کیبورد
```

### پشتیبان‌گیری از پیکربندی و cron

```powershell
.\weft.exe config export -o backup.yaml    # خروجی کامل پیکربندی در حال اجرا
.\weft.exe config import backup.yaml       # بازنویسی weft.yaml (نیاز به ری‌استارت)
.\weft.exe cron list                       # زمان‌بندی cleanup/benchmark/health_scan
.\weft.exe cron run cleanup                # اجرای فوری بدون صبر برای زمان‌بندی
```

---

## ۹) مقصدهای خروجی (Storage)

### مقصد پیش‌فرض (محلی)
بدون `--destination`، خروجی در `storage.local.base_path` (پیش‌فرض `./data`)
ذخیره می‌شود: `data/<job-id>/…`.

### یک سرور، چند پوشه (path)
هر سرور یک root (`base-path`) دارد؛ با فلگ `--path` در هر job یک زیرپوشه
انتخاب می‌کنی — مناسب برای movie/، series/، … زیر یک سرور:

```bash
# سرور id 2 با root یک‌بار ثبت می‌شود
weft storage add 2 ssh --host server.example.com --user root \
    --password 'secret' --base-path /var/videos

# سپس هر job پوشه خودش را می‌گیرد:
weft jobs create "local:/src/series/ep01.mp4" --profile vod-h264 --destination 2 --path series
weft jobs create "local:/src/film.mp4"        --profile vod-h264 --destination 2 --path movie
```

خروجی: `/var/videos/series/<job-id>/…` و `/var/videos/movie/<job-id>/…`

### سرور SSH (با کلید یا با یوزر/پسورد)
```powershell
# با کلید
.\weft.exe storage add 1 ssh --host server.example.com --user root `
    --key-path "C:\keys\id_weft" --base-path /srv/weft

# با یوزر/پسورد (بدون نیاز به کلید)
.\weft.exe storage add 1 ssh --host server.example.com --user root `
    --password "secret" --base-path /srv/weft --port 22
```

### S3 / MinIO / R2
```powershell
.\weft.exe storage add 2 s3 --host s3.example.com --bucket media `
    --region us-east-1 --access-key AKIA... --secret-key ...
```

### سپس برای هر job
```powershell
.\weft.exe jobs create "D:\videos\film.mp4" --profile vod-h264 --destination 1
```

> `destination_id` صفر = مقصد محلی پیش‌فرض.

---

## ۱۰) کلیدهای API و امنیت

اگر `security.api_keys: true` باشد، همه درخواست‌ها به توکن نیاز دارند.

```powershell
# ساخت کلید (کلید خام فقط یک‌بار نمایش داده می‌شود!)
.\weft.exe keys create mykey "jobs:read" "jobs:write"

.\weft.exe keys list
.\weft.exe keys delete <key-id>
```

**Scope ها:** `jobs:read` `jobs:write` `queue:read` `workers:read`
`storage:manage` `webhooks:manage` `profiles:read` `plugins:read`
`metrics:read` `keys:manage`

استفاده از کلید اختصاصی:
```powershell
.\weft.exe jobs list --key "wft_live_xxxx"
```

---

## ۱۱) Webhook (اعلان به سیستم شما)

```powershell
# برای رویدادهای خاص
.\weft.exe webhooks create "http://myserver/hook" "job.completed" "job.failed" --secret mysecret

# برای همه رویدادها
.\weft.exe webhooks create "http://myserver/hook" "*"

.\weft.exe webhooks list
.\weft.exe webhooks delete <webhook-id>
```

رویدادها: `job.created`, `job.started`, `job.progress`, `job.completed`,
`job.failed`, `job.cancelled`, `job.paused`, `job.resumed`, `plugin.finished`

اگر `webhook_signing: hmac-sha256` فعال باشد، هدر
`X-Weft-Signature: HMAC-SHA256(secret, body)` ارسال می‌شود.

---

## ۱۲) نگهداری و عیب‌یابی

### چک‌آپ سلامت
```powershell
.\weft.exe doctor
.\weft.exe version
```

### لاگ‌ها
- همه عملیات در خروجی استاندارد دیمن لاگ می‌شود (HTTP request ها + worker errors).
- در ویندوز برای نگهداری لاگ:
  ```powershell
  Start-Process weft.exe -ArgumentList "serve","--config","weft.yaml" `
      -RedirectStandardOutput "weft.log" -RedirectStandardError "weft.err" -WindowStyle Hidden
  ```

### مشکلات رایج
| مشکل | راه‌حل |
|---|---|
| job فیل شده: `ffmpeg: executable file not found` | ffmpeg را نصب یا در `executor.ffmpeg_path` مسیرش را بده |
| job فیل شده: `Stream map '' matches no streams` | ورودی صدا ندارد — این حالت خودکار مدیریت می‌شود (بدون `-map 0:a:0`) |
| job فیل شده: `ssh ... Connection refused` | دسترسی SSH را چک کن (کلید/پسورد، پورت، host) |
| دیتابیس خراب/قدیمی | `weft.db` را نگه دار؛ Weft ستون‌های جدید را خودکار اضافه می‌کند |
| job فیل شده: `whisper produced no srt` | whisper-cli را نصب کن (`whisper-cli` در PATH) + `whisper.model_path` را بده؛ stderr واقعی whisper در error job است |
| job فیل شده: `ai_subtitle: hybrid requires gemini.api_key` | برای `--provider hybrid` کلید gemini لازم است (ترجمه) — `gemini.api_key` را بده |
| دسترسی از بیرون نمی‌شود | `network.listen: 0.0.0.0:8443` + تنظیم فایروال |
| همه job ها در صف مانده‌اند | `workers.min` را افزایش بده یا `scheduler.max_cpu_percent` را بالا ببر |

### تمیزکاری خودکار (cron داخلی)
کانفیگ `cron` در weft.yaml به‌صورت خودکار:
- پاکسازی رکوردهای قدیمی دیتابیس (`cleanup.retention_hours`)
- اگه `cleanup.delete_files: true` هم تنظیم شده باشه، علاوه بر رکورد دیتابیس، فایل سورس محلی (مثلاً زیر `/var/Source`) و پوشه‌های کاری باقی‌مونده‌ی همون job هم پاک می‌شن — پیش‌فرض خاموشه، جزئیات کامل تو `docs/REFERENCE.md` بخش Configuration
- اجرای بنچمارک هفتگی
- اسکن سلامت هر ۵ دقیقه

---

## ۱۳) چک‌لیست نصب (خلاصه برای ادمین)

```
[ ]  ffmpeg + ffprobe نصب و در PATH
[ ]  weft.exe در پوشه دائمی (مثل C:\weft)
[ ]  weft init-config
[ ]  ویرایش weft.yaml:
        - admin_api_key  (کلید قوی)
        - network.listen (اگر از بیرون نیاز است)
        - workers.min    (ظرفیت همزمان)
        - storage.local.base_path
[ ]  weft doctor  →  خروجی 0
[ ]  weft serve --config weft.yaml   (به‌عنوان سرویس/در پس‌زمینه)
[ ]  ساخت کلید API:  weft keys create admin "jobs:read" "jobs:write" ...
[ ]  ثبت مقصد (اختیاری):
        weft storage add 1 ssh --host ... --user ... --password ... --base-path ...
[ ]  تست نهایی:
        weft jobs create "D:\test\film.mp4" --profile vod-h264
        weft jobs get <job-id>   →  completed
[ ]  بررسی خروجی:  data/<job-id>/playlist.m3u8 و thumbnails/ و …
```

---

## ۱۴) ساختار خروجی نهایی (مرجع)

خروجی هر job در `data/<job-id>/` (یا روی سرور مقصد):

```
data/<job-id>/
├── playlist.m3u8                ← master playlist (برای پخش مستقیم)
├── 360p.m3u8
├── 360p_000.ts … 360p_004.ts    ← segment های هر رندیشن کنار m3u8 آن
├── 480p.m3u8  /  480p_000.ts …
├── 720p.m3u8  /  720p_000.ts …
├── 1080p.m3u8 /  1080p_000.ts …
├── thumbnails/
│   ├── <name>_poster.jpg
│   ├── <name>_sprite.jpg
│   └── <name>_preview.vtt
├── subtitle/<lang>/             ← هر زبان در پوشه خودش (fa, en, …)
│   └── <name>.vtt
├── audio/                       ← فقط برای ورودی صوتی
│   └── <name>.m3u8
│       └── <name>_NNN.ts
```

- m3u8 ها و ts ها **در یک‌دایرکتوری** هستند (مسیرهای نسبی داخل playlists
  همیشه درست کار می‌کنند).
- برای پخش کافی است پوشه job را به عنوان ریشه HLS به پلیر بدهید:
  `http://<host>/data/<job-id>/playlist.m3u8`

### ساختار بعد از افزودن زیرنویس/دوبله (`subtitle-add` / `dub-add`)
master playlist (`<name>_master.m3u8`) به‌روزرسانی می‌شود تا ترک‌های جدید
انتخاب‌پذیر شوند (بدون پردازش مجدد ویدیو):

```
Series-Test/movie1/
├── movie_master.m3u8          ← ویرایش می‌شود: SUBTITLES/AUDIO + گروه
├── movie/720p/movie.m3u8 …
├── subtitle/fa/movie.vtt      ← زیرنویس فارسی (subtitle-add)
├── subtitle/en/movie.vtt      ← ترک دوم (زبان جدید = افزودن)
└── audio/fa/movie.m3u8        ← دوبله (dub-add)
```

مثال master پس از دو بار `subtitle-add` (fa و en):

```
#EXTM3U
#EXT-X-VERSION:3
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="فارسی",DEFAULT=NO,AUTOSELECT=YES,FORCED=NO,LANGUAGE="fa",URI="subtitle/fa/movie.vtt"
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="English",DEFAULT=NO,AUTOSELECT=YES,FORCED=NO,LANGUAGE="en",URI="subtitle/en/movie.vtt"
#EXT-X-STREAM-INF:BANDWIDTH=3000000,RESOLUTION=1280x720,SUBTITLES="subs"
movie/720p/movie.m3u8
```

### ۱۵) بازسازی master از روی فایل‌ها (`weft storage rebuild`)

اگر `playlist.m3u8` گم یا خراب شود (مثلاً بعد از ویرایش دستی)، نیازی به
اجرای دوباره job نیست. این فرمان storage را اسکن می‌کند و master را فقط از
روی چیزهایی که واقعاً منتشر شده‌اند بازسازی می‌کند: رندیشن‌های ریشه
(`360p.m3u8` …)، زیرنویس‌های `subtitle/<lang>/*.vtt` و دوبله‌های
`audio/<lang>/*.m3u8`:

```
weft storage rebuild --destination 2 --path Series-Test/movie1
weft storage rebuild --path movie1            # destination 0 = local
```

نکته‌ها:

- رندیشن‌هایی که در ریشه نیستند (نام‌های دیگر) نادیده گرفته می‌شوند؛
  حداقل یک `*.m3u8` با نام ladder (360p/480p/720p/1080p) لازم است.
- ترک‌های زیرنویس/دوبله بر اساس زبان مرتب می‌شوند و به `SUBTITLES="subs"`
  و `AUDIO="audio"` در هر `EXT-X-STREAM-INF` متصل می‌شوند.
- `playlist.m3u8` بازنویسی می‌شود (اتمی در محل همان storage).
- دسترسی: scope `storage:manage`.
