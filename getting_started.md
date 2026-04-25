# Getting Started — UE Crash Reporter

A self-hosted crash ingestion and triage service for Unreal Engine projects.  
Receives crash reports from **CrashReportClient**, stores them in SQLite, and exposes a web dashboard.

---

## 1. Deploy the service

### Option A — Docker (recommended)

```bash
git clone <repo-url> ue-crash-reporter
cd ue-crash-reporter
docker compose up -d
```

The service listens on **port 8080** by default. Crash files and the SQLite database are stored in the `crash_data` Docker volume.

To change the port, edit `docker-compose.yml`:

```yaml
ports:
  - "9000:8080"   # host:container
```

### Option B — Build from source

```bash
# First-time setup: download dependencies and initialise the git repo
bash setup.sh          # Linux / macOS
# .\setup.ps1          # Windows PowerShell

# Run the server
go run ./cmd/server
# or: go build -o ue-crash-reporter ./cmd/server && ./ue-crash-reporter
```

Requires **Go 1.22+**. No external runtime dependencies — SQLite is bundled.

### Environment variables

| Variable   | Default              | Description                         |
|------------|----------------------|-------------------------------------|
| `ADDR`     | `:8080`              | `host:port` the HTTP server binds to |
| `DATA_DIR` | `./data`             | Where crash files are stored on disk |
| `DB_PATH`  | `$DATA_DIR/crashes.db` | SQLite database path               |

---

## 2. Verify it's running

```bash
curl http://localhost:8080/health
# → ok
```

Open the dashboard in a browser: **http://localhost:8080**

---

## 3. Hook it up to your UE project

UE's crash reporter reads its upload URL from `DefaultEngine.ini`.

### Step 1 — Find (or create) `DefaultEngine.ini`

Located at:
```
<ProjectRoot>/Config/DefaultEngine.ini
```

### Step 2 — Add the `[CrashReportClient]` section

```ini
[CrashReportClient]
CrashReportClientVersion=1
DataRouterUrl="http://<your-server-ip>:8080/api/v1/crash"
```

Replace `<your-server-ip>` with your server's LAN IP or domain name.  
For local testing use `http://127.0.0.1:8080/api/v1/crash`.

### Step 3 — Enable crash reporting in your build

In `DefaultEngine.ini` (or your platform-specific override):

```ini
[/Script/Engine.CrashReportCoreSettings]
bSendUnattendedBugReports=True
```

For **Shipping** builds you may also want:

```ini
[CrashReportClient]
bHideLogFilesOption=True
bSendLogFile=True
```

### Step 4 — Trigger a test crash

In a Development or DebugGame build, open the console and run:

```
crash
```

Or from C++:

```cpp
UE_LOG(LogTemp, Fatal, TEXT("Test crash"));
```

Watch the dashboard refresh — the crash should appear within a few seconds.

---

## 4. What gets stored

Each crash submission stores:

- Parsed metadata from `CrashContext.runtime-xml` (game name, platform, build version, call stack, error message)
- All files sent by CrashReportClient: `.dmp` minidump, game log, `Diagnostics.txt`, etc.
- Files are downloadable from the crash detail page

---

## 5. Production checklist

- [ ] Put the service behind a reverse proxy (nginx / Caddy) with TLS so crash data is encrypted in transit
- [ ] Set `DataRouterUrl` to your public `https://` URL  
- [ ] Mount `crash_data` volume to durable storage (not ephemeral container storage)
- [ ] Set up log rotation or a cron job to prune old crash files from `DATA_DIR`
- [ ] Restrict dashboard access with HTTP basic auth or VPN — the dashboard has no auth by default

---

## 6. API reference

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/crash` | Receive a crash report (multipart/form-data) |
| `POST` | `/api/v2/crash` | Alias for v1 |
| `GET`  | `/` | Dashboard — crash list |
| `GET`  | `/crash/{id}` | Crash detail page |
| `GET`  | `/crash/{id}/file/{filename}` | Download attached file |
| `GET`  | `/health` | Health check — returns `ok` |
