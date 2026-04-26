# Pattern Language — UE Crash Reporter

*In the spirit of Christopher Alexander's* A Pattern Language*, each pattern below names a recurring design problem in this codebase, states the forces in tension, and prescribes the solution form that resolves them.*

---

## Pattern 1 — Single Durable Store

**Context:** A crash reporter must survive restarts, container migrations, and disk re-mounts without losing data.

**Problem:** Multiple storage backends (files, a remote DB, an in-memory cache) spread state across systems that can drift out of sync. A crash record with no corresponding files — or files with no record — is useless.

**Forces:**
- Operators want zero-dependency deploys
- Crash data must survive process restarts
- Files are large; SQL rows are small — they must stay linked

**Solution:** Keep one SQLite database and one data directory, always co-located. The database holds metadata; the directory holds binary files. The database row is the authoritative identity; files reference back to it via `store_path`. If a row exists, its files exist. If files exist without a row, they are orphans and can be pruned.

**Resulting context:** `storage.Store` is the only writer. No caching layer. Backup = copy `/data/`.

---

## Pattern 2 — Tolerant Crash Receiver

**Context:** UE's CrashReportClient sends whatever files it has at the moment of crash. The set varies by platform, build config, and UE version.

**Problem:** A strict schema (expecting exactly these files in exactly this format) breaks on any deviation — older UE builds, stripped shipping builds, custom project configurations.

**Forces:**
- CrashContext.runtime-xml is the richest source of metadata but may be absent
- Minidumps may be corrupted if the crash was severe enough to corrupt heap
- We must never return an error that causes UE to discard the report entirely

**Solution:** Accept any multipart upload. Parse `CrashContext.runtime-xml` if present; populate fields from it but degrade gracefully to empty strings if absent or malformed. Persist every file unconditionally. Respond `200 OK` even when parsing fails — the files are still on disk. Never reject a report for missing fields.

**Resulting context:** `receiveCrash` in `crash_handler.go` never returns 4xx for malformed content, only for request-level problems (body too large). `parseCrashContext` is a best-effort helper, not a gatekeeper.

---

## Pattern 3 — GUID-Keyed Deduplication

**Context:** UE may retry a crash submission if the first attempt times out or the client receives no acknowledgement.

**Problem:** Duplicate rows pollute the dashboard and inflate crash counts.

**Forces:**
- The GUID in `CrashContext.runtime-xml` is stable across retries for the same crash event
- Without a GUID (older builds), we must still accept the report
- Rejecting duplicates should be silent, not an error

**Solution:** `StoreCrash` checks for an existing row with the same GUID before inserting. If found, it returns the existing ID and exits without modifying any data. If no GUID is available, a timestamp-derived fallback is used — this may produce duplicates, but that is preferable to losing reports.

**Resulting context:** The `guid` column has a `UNIQUE` constraint. Idempotent retries are free. The fallback GUID path is a known limitation noted in comments.

---

## Pattern 4 — Environment-Driven Configuration

**Context:** The service must run identically in a developer's local terminal, a Docker container, and a cloud VM without code changes.

**Problem:** Hardcoded paths or ports make deployment fragile and force rebuilds for configuration changes.

**Forces:**
- Docker volumes require configurable mount paths
- Port conflicts differ by environment
- Secrets (future: webhook URLs, auth tokens) must not be in source

**Solution:** All configuration is read from environment variables in `main.go` with safe defaults. No config file format to parse. The Dockerfile sets the same env vars as defaults, so the binary runs identically inside and outside the container.

**Resulting context:** `envOr(key, fallback)` is the only config primitive. `docker-compose.yml` environment block is the single source of truth for production values.

---

## Pattern 5 — Embedded UI, No Build Step

**Context:** The dashboard must ship inside a single binary for simple deployment.

**Problem:** A separate frontend build pipeline (npm, webpack, CDN) adds operational complexity and failure points for what is fundamentally a triage tool, not a consumer product.

**Forces:**
- Operators want `docker pull && docker run` — not a multi-service compose
- Templates change infrequently; a hot-reload dev loop is not required
- The UI need not be beautiful, but must be readable under stress

**Solution:** HTML templates are embedded into the Go binary via `//go:embed`. All styles are inline `<style>` blocks — no external CSS framework, no CDN dependency. Template functions (`kb`, `base`) live in `server.go` as a `template.FuncMap`.

**Resulting context:** The binary is fully self-contained. Template changes require a rebuild, which is acceptable given the low change frequency.

---

## Pattern 6 — Path-Contained File Downloads

**Context:** The service serves arbitrary files uploaded by a game client from `DATA_DIR`.

**Problem:** If a filename contains `../`, a malicious or buggy client could cause the server to serve files outside `DATA_DIR` — including the database or host OS files.

**Forces:**
- Filenames come from untrusted external input
- `filepath.Clean` alone is not sufficient without a containment check
- The check must happen at serve time, not at ingest time

**Solution:** `isSubPath(parent, child)` in `dashboard_handler.go` cleans both paths and verifies the file path begins with `DATA_DIR + separator`. Any file that fails this check returns `403 Forbidden`. Files are looked up by matching the `Filename` field stored in the database — the caller cannot choose an arbitrary path.

**Resulting context:** File traversal is blocked at two levels: the filename is matched against the database (not the filesystem), and the resolved path is checked against the data directory root.

---

## Pattern 7 — Graceful Shutdown with Signal Forwarding

**Context:** The service runs as a Docker container and must flush in-flight requests before stopping.

**Problem:** A hard `SIGKILL` mid-write can corrupt the SQLite WAL or leave an incomplete file on disk.

**Forces:**
- SQLite WAL mode is resilient but not crash-proof during an active write transaction
- Container orchestrators send `SIGTERM` before `SIGKILL` — we must honor it
- The shutdown window must be bounded (no hang forever)

**Solution:** `main.go` listens for `SIGINT`/`SIGTERM`, calls `http.Server.Shutdown` with a 10-second context, then closes the store. The Dockerfile uses `tini` as PID 1 to ensure signals are forwarded correctly from the container runtime.

**Resulting context:** Clean shutdown takes at most 10 seconds. In-flight crash uploads complete. The database is closed cleanly before the process exits.
