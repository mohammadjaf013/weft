# Weft — CLI و REST API
### راهنمای یکپارچهسازی و فراخوانی دستی

این سند فقط **راه‌های ارتباط با Weft** را شرح می‌دهد: دو واسط CLI و REST
API، این که هر فرمان CLI چه درخواست HTTP می‌فرستد، و بدنه دقیق
درخواست/پاسخ هر نقطه پایانی — تا بتوانید بدون CLI مستقیماً با API حرف
بزنید (اسکریپت، کد، curl، …).

---

## ۱) دو راه ارتباطی

```
┌──────────────┐   HTTP/JSON (Bearer token)   ┌──────────────┐
│  CLI  weft   │ ─────────────────────────────▶ │  REST API    │──▶ پردازش
│  (فرمان‌های   │                               │  (chi, port)  │
│   آماده)      │ ◀───────────────────────────── │              │
└──────────────┘                                └──────────────┘
   ↑                                    
   کاربر / اسکریپت / سیستم شما
```

- **CLI** فقط یک client سبک است؛ هیچ کار محاسباتی نمی‌کند. هر فرمان آن
  تبدیل به یک درخواست HTTP به دیمن می‌شود.
- **REST API** تنها راه ورود است — CLI از همان API استفاده می‌کند.
- بنابراین: هر کاری که CLI بتواند بکند، API هم می‌تواند (و برعکس).

---

## ۲) اتصال پایه (هم برای CLI هم API)

### پیش‌فرض‌های CLI
| چیز | پیش‌فرض | از کجا |
|---|---|---|
| آدرس API | `http://127.0.0.1:8443` | `network.listen` در `weft.yaml` |
| توکن (Bearer) | `security.admin_api_key` | `weft.yaml` |

این دو مقدار را CLI خودکار از `weft.yaml` می‌خواند — پس اگر دیمن روی همان
ماشین است، کافی است `weft` را صدا بزنید.

### اتصال به دیمن دیگر / با کلید دیگر
```powershell
.\weft.exe jobs list --api http://192.168.1.50:8443 --key "wft_live_xxxx"
```

---

## ۳) احراز هویت (Authentication)

### کلید خام (Bearer token)
هر درخواست باید هدر داشته باشد:
```
Authorization: Bearer <api_key>
```

### ساخت کلید
```powershell
.\weft.exe keys create mykey "jobs:read" "jobs:write"
# خروجی:  { "id": "key_xxx", "key": "wft_live_..." }   ← کلید خام فقط همین‌جا!
```

معادل API:
```
POST /keys
{
  "name": "mykey",
  "scopes": ["jobs:read", "jobs:write"]
}
→ 201  { "id": "key_xxx", "key": "wft_live_..." }
```

### Scope ها (سطوح دسترسی)
| Scope | اجازه می‌دهد |
|---|---|
| `jobs:read` | مشاهده job ها و رویدادها |
| `jobs:write` | ساخت job، cancel/retry/pause/resume |
| `queue:read` | مشاهده صف |
| `workers:read` | مشاهده worker ها |
| `storage:manage` | فهرست/ثبت سرورهای مقصد |
| `webhooks:manage` | مدیریت webhook |
| `profiles:read` | فهرست پروفایل‌ها |
| `plugins:read` | فهرست پلاگین‌ها |
| `metrics:read` | متریک‌ها و benchmark |
| `keys:manage` | مدیریت کلیدهای API |

### کدهای خطای احراز هویت
| وضعیت HTTP | معنی |
|---|---|
| `401` | توکن نیست/نادرست است → `{"error":{"code":"unauthorized",...}}` |
| `403` | کلید معتبر است ولی scope کافی ندارد → `{"error":{"code":"forbidden",...}}` |

> اگر `security.api_keys: false` باشد، هیچ توکنی لازم نیست (فقط در شبکه
> امن استفاده کنید).

---

## ۴) بدنه خطای مشترک

تمام خطاهای API به این شکل برمی‌گردند:
```json
{ "error": { "code": "unknown_profile", "message": "profile \"x\" not found" } }
```

---

## ۵) مرجع کامل نقاط پایانی (Endpoints)

### عمومی (بدون توکن)

| متد | مسیر | توضیح |
|---|---|---|
| GET | `/health` | سلامت دیمن → `{"status":"ok"}` |
| GET | `/` | معرفی سرویس: نسخه، پروفایل‌ها، پلاگین‌ها، فهرست endpoint ها |

### Job ها — `jobs:read` / `jobs:write`

| متد | مسیر | Scope | توضیح |
|---|---|---|---|
| GET | `/jobs` | `jobs:read` | فهرست job ها (فیلتر: `?status=&priority=&limit=`) |
| POST | `/jobs` | `jobs:write` | ساخت job جدید |
| GET | `/jobs/{id}` | `jobs:read` | جزئیات + وضعیت task ها |
| GET | `/jobs/{id}/events` | `jobs:read` | زنجیره رویدادها |
| POST | `/jobs/{id}/{action}` | `jobs:write` | اکشن: `cancel` / `retry` / `pause` / `resume` |

**ساخت job** (POST `/jobs`):
```json
{
  "input_ref": "D:\\videos\\film.mp4",
  "profile": "vod-h264",
  "destination_id": 1,
  "priority": "high",
  "lang": "fa",
  "src_lang": "en",
  "provider": "hybrid",
  "path": "series"
}
```
| فیلد | لازم | توضیح |
|---|---|---|
| `input_ref` | ✅ | مسیر فایل محلی (یا `local:C:\...`) |
| `profile` | ✅ | نام پروفایل (مثلاً `vod-h264`) |
| `destination_id` | ❌ | `0`=محلی (پیش‌فرض)، یا شناسه سرور ثبت‌شده |
| `priority` | ❌ | `emergency`/`high`/`normal`/`low`/`background` (پیش‌فرض `normal`) |
| `lang` | ❌ | زبان مقصد زیرنویس، مثل `fa`/`en` |
| `src_lang` | ❌ | زبانِ صدا (whisper `-l`)، مثلاً `en`/`tr`؛ اگر با `lang` فرق کند، hybrid **ترجمه** می‌کند |
| `provider` | ❌ | `whisper`/`gemini`/`hybrid` برای پروفایل ai-subtitle (خالی = پیش‌فرض سرور) |
| `path` | ❌ | زیرپوشه زیر root مقصد (مثلاً `movie` یا `series`) — یک سرور با چند پوشه |

**پاسخ** (201):
```json
{
  "id": "job_xxx",
  "status": "queued",
  "tasks": ["task_yyy", "..."]
}
```

**نکته:** `input_ref` را می‌توان با شکل URI هم داد:
`local:G:\videos\film.mp4` یا `local:/srv/weft/film.mp4` (روی لینوکس).

### صف و Worker — `queue:read` / `workers:read`

| متد | مسیر | Scope | توضیح |
|---|---|---|---|
| GET | `/queue` | `queue:read` | تعداد job در هر priority |
| GET | `/workers` | `workers:read` | وضعیت worker ها (busy/idle + task جاری) |

### Storage — `storage:manage`

| متد | مسیر | Scope | توضیح |
|---|---|---|---|
| GET | `/storage/servers` | `storage:manage` | فهرست مقصدها (بدون نمایش کلید/پسورد) |
| POST | `/storage/servers` | `storage:manage` | ثبت مقصد جدید |
| POST | `/storage/rebuild-master` | `storage:manage` | بازسازی `playlist.m3u8` از فایل‌های روی دیسک |

**ثبت مقصد** (POST `/storage/servers`):
```json
{
  "id": 1,
  "type": "ssh",
  "host": "server.example.com",
  "user": "root",
  "config": {
    "port": 22,
    "base_path": "/srv/weft",
    "key_path": "C:\\keys\\id_weft"
  }
}
```
انواع: `ssh` | `local` | `s3` | `minio` | `r2`

- برای SSH: یا `config.key_path` یا `config.password` (دیگر نیازی به کلید نیست).
- برای S3/MinIO/R2: `config.bucket`, `config.region`, `config.access_key`,
  `config.secret_key`, `config.base_path`.

**پاسخ:** `201  { "id": 1, "status": "registered" }`

> توجه: API همیشه مقصدها را **بدون** کلید/پسورد برمی‌گرداند.

**بازسازی master** (POST `/storage/rebuild-master`): وقتی `playlist.m3u8` روی
دیسک گم/خراب شده باشد (مثلاً توسط نسخه قدیمی)، از فایل‌های موجود
(360p.m3u8 …، `subtitle/<lang>/<name>.vtt`، `audio/<lang>/<name>.m3u8`)
پلی‌لیست master را دوباره می‌سازد:
```json
{ "destination_id": 2, "path": "Series-Test/movie1" }
→ 200  { "status":"ok", "renditions":["720p","1080p"], "subtitles":[{"lang":"fa",...}], ... }
```

### Webhook — `webhooks:manage`

| متد | مسیر | Scope | توضیح |
|---|---|---|---|
| GET | `/webhooks` | `webhooks:manage` | فهرست |
| POST | `/webhooks` | `webhooks:manage` | ساخت |
| DELETE | `/webhooks/{id}` | `webhooks:manage` | حذف |
| POST | `/webhooks/{event_id}/replay` | `webhooks:manage` | ارسال دوباره رویدادهای dead-letter |

**ساخت** (POST `/webhooks`):
```json
{
  "url": "http://myserver/hook",
  "secret": "mysecret",
  "events": ["job.completed", "job.failed"]
}
```
> `events: ["*"]` یعنی همه رویدادها.

**پاسخ:** `201  { "id": "wh_xxx" }`

رویدادها: `job.created`, `job.started`, `job.progress`, `task.progress`,
`job.completed`, `job.failed`, `job.cancelled`, `job.paused`, `job.resumed`,
`plugin.finished`

### کلیدهای API — `keys:manage`

| متد | مسیر | Scope | توضیح |
|---|---|---|---|
| GET | `/keys` | `keys:manage` | فهرست (بدون کلید خام) |
| POST | `/keys` | `keys:manage` | ساخت (کلید خام فقط همین‌جا) |
| DELETE | `/keys/{id}` | `keys:manage` | حذف |

### اطلاع‌رسانی — `profiles:read` / `plugins:read`

| متد | مسیر | Scope | توضیح |
|---|---|---|---|
| GET | `/profiles` | `profiles:read` | فهرست پروفایل‌ها |
| GET | `/plugins` | `plugins:read` | فهرست پلاگین‌ها و انواع task |

### متریک‌ها و بنچمارک — `metrics:read`

| متد | مسیر | Scope | توضیح |
|---|---|---|---|
| GET | `/metrics` | `metrics:read` | خروجی متنی Prometheus (فرمت `text/plain`) |
| POST | `/benchmark` | `metrics:read` | اجرای بنچمارک CPU/ffmpeg → `{cpu_score, ffmpeg_score}` |
| GET | `/benchmark` | `metrics:read` | آخرین نتیجه بنچمارک |

---

## ۶) مثال‌های مستقیم با API (بدون CLI)

### با PowerShell
```powershell
$key = "wft_live_xxxx"
$h = @{ Authorization = "Bearer $key"; "Content-Type" = "application/json" }

# ساخت job
$body = '{"input_ref":"D:\\videos\\film.mp4","profile":"vod-h264","priority":"high"}'
Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:8443/jobs" -Headers $h -Body $body

# دیدن وضعیت
Invoke-RestMethod -Uri "http://127.0.0.1:8443/jobs/job_xxx" -Headers $h

# لغو
Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:8443/jobs/job_xxx/cancel" -Headers $h
```

### با curl
```bash
curl -H "Authorization: Bearer wft_live_xxxx" \
     http://127.0.0.1:8443/jobs

curl -X POST http://127.0.0.1:8443/jobs \
     -H "Authorization: Bearer wft_live_xxxx" \
     -H "Content-Type: application/json" \
     -d '{"input_ref":"/srv/weft/film.mp4","profile":"vod-h264"}'
```

---

## ۷) جدول تطبیق CLI ← API

هر فرمان CLI دقیقاً همین درخواست را می‌فرستد:

| فرمان CLI | درخواست HTTP |
|---|---|
| `weft jobs list` | `GET /jobs` |
| `weft jobs get <id>` | `GET /jobs/{id}` |
| `weft jobs events <id>` | `GET /jobs/{id}/events` |
| `weft jobs create <input> --profile <p>` | `POST /jobs` |
| `weft jobs log <id> <task>` | `GET /jobs/{id}/tasks/{task}/log` |
| `weft jobs asset <id> <name>` | `GET /jobs/{id}/assets/{name}` |
| `weft jobs delete <id>` | `DELETE /jobs/{id}` |
| `weft jobs action <id> cancel` | `POST /jobs/{id}/cancel` |
| `weft workers scale <count>` | `POST /workers/scale` |
| `weft queue` | `GET /queue` |
| `weft workers` | `GET /workers` |
| `weft storage list` | `GET /storage/servers` |
| `weft storage add …` | `POST /storage/servers` |
| `weft storage rebuild --path …` | `POST /storage/rebuild-master` |
| `weft webhooks list` | `GET /webhooks` |
| `weft webhooks create …` | `POST /webhooks` |
| `weft webhooks delete <id>` | `DELETE /webhooks/{id}` |
| `weft keys create …` | `POST /keys` |
| `weft keys list` | `GET /keys` |
| `weft keys delete <id>` | `DELETE /keys/{id}` |
| `weft profiles` | `GET /profiles` |
| `weft plugins` | `GET /plugins` |
| `weft metrics` | `GET /metrics` |
| `weft benchmark` | `POST /benchmark` |

فلگ‌های مشترک CLI: `--api <url>` و `--key <token>` و `--config <path>`.

### برش (trim) و تامبنیل سفارشی هنگام ساخت job

`weft jobs create` فلگ‌های بیشتری می‌پذیرد:

| فلگ | معنی |
|---|---|
| `--trim-start <s>` | N ثانیه از ابتدای کلیپ قبل از HLS حذف شود (مثلاً `50` یعنی از ثانیه ۵۰ شروع شود) |
| `--trim-end <s>` | N ثانیه از انتهای کلیپ حذف شود (مثلاً `10` یعنی ۱۰ ثانیه آخر بریده شود) |
| `--thumb-count <n>` | به جای پوستر/اسپرایت/استیلز پیش‌فرض، دقیقاً n تامبنیل هم‌فاصله ساخته و آپلود شود |
| `--thumb-size <sz>` | اندازه تامبنیل: `1080x1080` یا `original` (فقط با `--thumb-count`) |

مثال: از ثانیه ۵۰ تا ۱۰ ثانیه مانده به انتها، با ۵ تامبنیل 1080×1080:

```
weft jobs create /in/movie.mp4 --profile vod-h264 \
  --trim-start 50 --trim-end 10 --thumb-count 5 --thumb-size 1080x1080
```

- تریم روی task های `hls` و `thumbnail` اعمال می‌شود تا تامبنیل‌ها با بازه
  بریده‌شده هماهنگ باشند.
- بعد از پایان job، تامبنیل‌ها در پاسخ `GET /jobs/{id}` (لیست `assets`) و روی
  storage دیده می‌شوند. برای گرفتن یک تامبنیل به‌صورت base64:

```
weft jobs asset <job-id> thumbnails/<movie>_thumb_01.jpg
```

---

## ۸) نکات یکپارچه‌سازی

- **هر درخواست با کلید مناسب**: کلیدها scope دارند؛ کلید فقط‌خواندنی
  (`jobs:read`) نمی‌تواند job بسازد.
- **شناسه job را ذخیره کنید**: پاسخ `POST /jobs` فقط id می‌دهد؛ وضعیت را
  با `GET /jobs/{id}` یا از طریق webhook دنبال کنید.
- **webhook بهتر از polling است**: رویداد `job.completed` در لحظه رسیدن به
  حالت نهایی ارسال می‌شود (با امضای HMAC در هدر `X-Weft-Signature` و تلاش
  مجدد خودکار). از `POST /webhooks/{event_id}/replay` برای ارسال دوباره
  رویدادهای ازدست‌رفته استفاده کنید.
- **ساختار خروجی**: پس از `job.completed`، خروجی در پوشه job است
  (`data/<job-id>/playlist.m3u8` + `thumbnails/` + `subtitle/<lang>/`).
  اگر `destination_id>0` باشد، همان ساختار روی سرور مقصد آپلود می‌شود.
