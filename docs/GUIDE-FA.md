# راهنمای کامل Weft

Weft یک عامل پردازش رسانه است: فایل صوتی/ویدیو را می‌گیرد، با ffmpeg
پردازش می‌کند (HLS، thumbnail، زیرنویس، …) و خروجی را آپلود می‌کند.

باینری: `weft.exe` (ویندوز) — همه چیز با همین یک فایل انجام می‌شود.

---

## ۱) شروع سریع

```powershell
# 1. ساخت فایل پیکربندی پیش‌فرض (فقط بار اول)
.\weft.exe init-config

# 2. بررسی محیط (ffmpeg، ffprobe، پیکربندی، دیتابیس)
.\weft.exe doctor

# 3. اجرای دیمن (سرویس)
.\weft.exe serve
```

> `serve` با `--config <path>` اگر weft.yaml جای دیگری باشد.
> اگر `weft.yaml` نباشد: اول `init-config` را اجرا کن.

بعد از بالا آمدن سرو، همه دستورهای دیگر (jobs، keys، …) از طریق HTTP به
همان دیمن وصل می‌شوند — پیش‌فرض‌ها خودکار از `weft.yaml` خوانده می‌شوند
(`network.listen` و `security.admin_api_key`).

---

## ۲) مدیریت Jobs (کار اصلی)

### ساخت Job
```powershell
# ساده‌ترین شکل: mp4 → HLS کامل + thumbnail + زیرنویس + آپلود
.\weft.exe jobs create "D:\videos\film.mp4" --profile vod-h264

# با priority و destination صریح
.\weft.exe jobs create "D:\videos\film.mp4" --profile vod-h264 --priority high --destination 0

# mp3 → m4a
.\weft.exe jobs create "D:\music\song.mp3" --profile audio

# mp3 → HLS صوتی (m4a + m3u8 + ts) + آپلود
.\weft.exe jobs create "D:\music\song.mp3" --profile audio-hls
```

**priority ها** (به ترتیب اولویت): `emergency` `high` `normal` `low` `background` — پیش‌فرض `normal`.

**ورودی** می‌تواند مسیر محلی باشد (ویندوز: `G:\...` یا `local:C:\...`) —
پسوندِ موجود نیست؛ ffprobe نوع را تشخیص می‌دهد.

### دیدن Job ها
```powershell
.\weft.exe jobs list                    # همه
.\weft.exe jobs list --status completed # فقط موفق‌ها
.\weft.exe jobs list --priority high    # فقط با priority بالا
.\weft.exe jobs list --limit 10         # محدود به ۱۰

.\weft.exe jobs get <job-id>            # جزئیات + وضعیت هر task
.\weft.exe jobs events <job-id>         # زنجیره رویدادها (created→started→…)
```

### کنترل Job
```powershell
.\weft.exe jobs action <job-id> cancel   # لغو
.\weft.exe jobs action <job-id> retry    # دوباره اجرا (از task های ناتمام)
.\weft.exe jobs action <job-id> pause    # توقف موقت
.\weft.exe jobs action <job-id> resume   # ادامه
```

> فلگ‌ها را می‌توانی بعد از آرگومان‌ها هم بنویسی:
> `.\weft.exe jobs get <id> --config weft.yaml --key mykey` — ترتیب مهم نیست.

### خروجی Job ها
دسترسی پیش‌فرض محلی: `data/<job-id>/` (در دایرکتوری اجرای دیمن).
مثال خروجی `vod-h264`:

```
data/job_xxxx/film_master.m3u8      ← master playlist (همه رندیشن‌ها)
data/job_xxxx/film_360p.m3u8        ← variant playlist
data/job_xxxx/film_360p_000.ts      ← segment ها
data/job_xxxx/film_720p.m3u8 + film_720p_000.ts
data/job_xxxx/film_1080p.m3u8 + film_1080p_000.ts
data/job_xxxx/film_poster.jpg       ← thumbnail
data/job_xxxx/film_sprite.jpg       ← تصویر شبکه‌ای (sprite)
data/job_xxxx/film_preview.vtt      ← فهرست زمانی thumbnail ها
```

---

## ۳) پروفایل‌ها

```powershell
.\weft.exe profiles    # فهرست همه پروفایل‌ها
```

| پروفایل | ورودی | خروجی |
|---|---|---|
| `vod-h264` | mp4/mkv/mov/… | HLS 4 رندیشن (360/480/720/1080p) + thumbnail + زیرنویس + master + آپلود |
| `vod-hevc` | mp4/mkv/mov/… | مثل بالا با کدک HEVC |
| `audio` | mp3/m4a/wav/flac/… | m4a + آپلود |
| `audio-hls` | mp3/m4a/wav/flac/… | m4a + HLS صوتی (m3u8 + ts) + آپلود |
| `thumbnail` | ویدیو | poster + sprite + vtt + آپلود |
| `ai-subtitle` | mp3/mp4/m4a/wav/flac | زیرنویس تولیدشده با AI (whisper/gemini) + آپلود |

> `ai-subtitle` نیاز به پیکربندی دارد: در `weft.yaml` یا whisper
> (`model_path`) یا gemini (`api_key`) را پر کن، وگرنه task فیل می‌شود.

---

## ۴) کلیدهای API (امنیت)

اگر `security.api_keys: true` باشد، دیمن همه درخواست‌ها را نیازمند توکن
می‌کند. توکن پیش‌فرض `security.admin_api_key` است (توسط CLI خودکار خوانده
می‌شود).

```powershell
# ساخت کلید (فقط یک‌بار نمایش داده می‌شود!)
.\weft.exe keys create mykey "jobs:read" "jobs:write"

.\weft.exe keys list              # فهرست کلیدها
.\weft.exe keys delete <key-id>   # حذف
```

**scope های موجود**: `jobs:read` `jobs:write` `queue:read` `workers:read`
`storage:manage` `webhooks:manage` `profiles:read` `plugins:read`
`metrics:read` `keys:manage`

استفاده از کلید اختصاصی با CLI:
```powershell
.\weft.exe jobs list --key "wft_live_xxxx"
```

---

## ۵) Webhook ها

```powershell
# ثبت webhook: برای رویدادهای خاص
.\weft.exe webhooks create "http://myserver/hook" "job.completed" "job.failed" --secret mysecret

# همه رویدادها
.\weft.exe webhooks create "http://myserver/hook" "*"

.\weft.exe webhooks list
.\weft.exe webhooks delete <webhook-id>
```

رویدادها: `job.created` `job.progress` `job.started` `job.completed`
`job.failed` `job.cancelled` `job.paused` `job.resumed` `plugin.finished` …

> اگر `webhook_signing: hmac-sha256` باشد، هدر `X-Weft-Signature` شامل
> HMAC-SHA256 امضای body با `--secret` است.

---

## ۶) Storage (مقصد آپلود)

```powershell
.\weft.exe storage list    # فهرست سرورهای مقصد
.\weft.exe storage add 1 ssh --host server.example.com --user root
.\weft.exe storage add 2 s3
.\weft.exe storage add 3 local
```

- `destination_id=0` → ذخیره محلی (`storage.local.base_path`).
- برای آپلود به سرور دیگر: `--destination <id>` در `jobs create`.

---

## ۷) مشاهده وضعیت

```powershell
.\weft.exe queue          # چند job در هر priority صف است
.\weft.exe workers        # وضعیت worker ها (busy/idle + task جاری)
.\weft.exe plugins        # پلاگین‌های فعال و انواع task ها
.\weft.exe metrics        # متریک‌های Prometheus
.\weft.exe benchmark      # اجرای بنچمارک CPU/ffmpeg
```

---

## ۸) نگهداری

```powershell
.\weft.exe doctor          # چک‌اپ کامل (خروجی 0=سالم، غیرصفر=مشکل)
.\weft.exe version         # نسخه
```

---

## ۹) نکات عملی

- **تک دیمن**: فقط یک `serve` را اجرا کن؛ همه CLI های دیگر همان را می‌زنند.
- **خروجی خطا**: همه خطاها به شکل `weft: ...` چاپ می‌شوند و کد خروجی غیرصفر
  می‌گیرند (برای اسکریپت‌نویسی).
- **امنیت**: `admin_api_key` را بگذار؛ بدون آن هر کسی که پورت را بداند می‌تواند
  job بسازد.
- **تمیزکاری**: دیتابیس `weft.db` و پوشه‌های `data/` و `work/` در محل اجرای
  دیمن ساخته می‌شوند.
