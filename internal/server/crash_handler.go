package server

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ue-crash-reporter/internal/models"
)

// crashContextXML mirrors the structure of UE's CrashContext.runtime-xml.
type crashContextXML struct {
	XMLName           xml.Name `xml:"FGenericCrashContext"`
	RuntimeProperties struct {
		CrashGUID       string `xml:"CrashGUID"`
		GameName        string `xml:"GameName"`
		PlatformName    string `xml:"PlatformName"`
		BuildVersion    string `xml:"BuildVersion"`
		EngineVersion   string `xml:"EngineVersion"`
		CrashType       string `xml:"Misc.CrashType"`
		ErrorMessage    string `xml:"ErrorMessage"`
		CallStack       string `xml:"CallStack"`
		UserDescription string `xml:"UserDescription"`
	} `xml:"RuntimeProperties"`
}

const maxUploadSize = 128 << 20 // 128 MB

// pendingFile holds a filename and its raw bytes collected from any transport.
type pendingFile struct {
	name string
	data []byte
}

// receiveCrash handles POST /api/v1/crash from UE's CrashReportClient.
//
// UE5 (5.3+) sends a single application/octet-stream body that is a zip
// archive containing crash files:
//   - CrashContext.runtime-xml  — structured crash metadata
//   - <GameName>.log            — game log
//   - <hash>.dmp               — minidump
//   - Diagnostics.txt          — system summary
//
// Older UE versions send multipart/form-data with the same files as parts.
// Both transports are handled here.
func (s *Server) receiveCrash(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	ct := r.Header.Get("Content-Type")

	var (
		pending []pendingFile
		err     error
	)

	switch {
	case strings.Contains(strings.ToLower(ct), "application/octet-stream"):
		// UE5.3+ sends a raw zip blob.
		pending, err = s.extractOctetStream(r)
		if err != nil {
			s.log.Printf("extract octet-stream: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

	case strings.Contains(strings.ToLower(ct), "multipart/form-data"):
		// Older UE sends individual files as multipart parts.
		pending, err = extractMultipart(r)
		if err != nil {
			s.log.Printf("extract multipart: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

	default:
		s.log.Printf("unrecognised Content-Type %q — attempting octet-stream fallback", ct)
		pending, err = s.extractOctetStream(r)
		if err != nil {
			s.log.Printf("fallback extract failed: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
	}

	if len(pending) == 0 {
		s.log.Printf("received empty crash report — ignoring")
		w.WriteHeader(http.StatusOK)
		return
	}

	s.log.Printf("received %d file(s): %s", len(pending), fileNames(pending))

	crash := &models.Crash{
		ReceivedAt: time.Now().UTC(),
		GUID:       fmt.Sprintf("manual-%d", time.Now().UnixNano()), // fallback GUID
	}

	// Parse CrashContext.runtime-xml first so we have the real GUID.
	for _, pf := range pending {
		if strings.EqualFold(pf.name, "crashcontext.runtime-xml") {
			parseCrashContext(pf.data, crash)
		}
	}

	// Persist files under data/<guid>/.
	crashDir := filepath.Join(s.dataDir, crash.GUID)
	if err := os.MkdirAll(crashDir, 0o755); err != nil {
		s.log.Printf("mkdir %s: %v", crashDir, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	for _, pf := range pending {
		dest := filepath.Join(crashDir, filepath.Base(pf.name))
		if err := os.WriteFile(dest, pf.data, 0o644); err != nil {
			s.log.Printf("write file %s: %v", dest, err)
			continue
		}
		rel, _ := filepath.Rel(s.dataDir, dest)
		crash.Files = append(crash.Files, models.CrashFile{
			Filename:  filepath.Base(pf.name),
			SizeBytes: int64(len(pf.data)),
			StorePath: rel,
		})
	}

	id, err := s.store.StoreCrash(crash)
	if err != nil {
		s.log.Printf("store crash: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.log.Printf("stored crash id=%d guid=%s game=%s platform=%s",
		id, crash.GUID, crash.GameName, crash.Platform)

	// UE expects a 200 with no specific body — just acknowledge.
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "crash %s stored (id=%d)\n", crash.GUID, id)
}

// extractOctetStream reads the body and extracts crash files from it.
//
// UE5 (5.3+) sends a zlib-compressed zip archive. The extraction pipeline is:
//  1. Read raw body bytes.
//  2. Attempt zlib decompression — UE deflates the payload before sending.
//  3. Attempt zip extraction on the (possibly decompressed) bytes.
//  4. Fall back to saving the decompressed (or raw) bytes as "raw.bin" so data is never lost.
func (s *Server) extractOctetStream(r *http.Request) ([]pendingFile, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(body) == 0 {
		return nil, nil
	}
	s.log.Printf("extractOctetStream: raw body %d bytes, magic %X", len(body), magic(body))

	// Step 1: try zlib decompression.
	payload := body
	if zr, err := zlib.NewReader(bytes.NewReader(body)); err != nil {
		s.log.Printf("extractOctetStream: zlib open failed: %v", err)
	} else {
		decompressed, err := io.ReadAll(zr)
		zr.Close()
		if err != nil {
			s.log.Printf("extractOctetStream: zlib read failed: %v", err)
		} else {
			s.log.Printf("extractOctetStream: zlib ok — decompressed %d -> %d bytes, magic %X",
				len(body), len(decompressed), magic(decompressed))
			payload = decompressed
		}
	}

	// Step 2a: try UE's "CR1" proprietary container format.
	if files := parseCR1(payload); len(files) > 0 {
		s.log.Printf("extractOctetStream: CR1 ok — %d file(s)", len(files))
		return files, nil
	}

	// Step 2b: try zip extraction on the (possibly decompressed) payload.
	if files := extractZip(payload); len(files) > 0 {
		s.log.Printf("extractOctetStream: zip ok — %d file(s)", len(files))
		return files, nil
	}
	s.log.Printf("extractOctetStream: unknown format — saving payload as raw.bin (magic %X)", magic(payload))

	// Step 3: fall back — save decompressed payload so "file" can identify the format.
	return []pendingFile{{name: "raw.bin", data: payload}}, nil
}

// magic returns up to the first 8 bytes of b for format identification logging.
func magic(b []byte) []byte {
	if len(b) > 8 {
		return b[:8]
	}
	return b
}

// parseCR1 parses UE5's proprietary "CR1" crash container format.
//
// After zlib decompression the payload has this layout:
//
//	Header section (variable length, contains crash GUID etc. — we skip it)
//	Directory header (12 bytes):
//	  [4] total_payload_size  — always equals len(p), used as the locator anchor
//	  [4] file_count
//	  [4] reserved (zeros)
//	File entries (file_count times):
//	  [4]           filename_len  (always 260 = Windows MAX_PATH)
//	  [filename_len] filename     (null-padded fixed-width string)
//	  [4]           data_len
//	  [data_len]    file data     (inline, no compression)
func parseCR1(p []byte) []pendingFile {
	if len(p) < 4 || string(p[:3]) != "CR1" {
		return nil
	}

	// Locate the directory header by scanning for a uint32 equal to len(p)
	// followed by a plausible file count (1–200).
	payloadLen := uint32(len(p))
	dirOff := -1
	for i := 0; i <= len(p)-12; i++ {
		if binary.LittleEndian.Uint32(p[i:i+4]) != payloadLen {
			continue
		}
		count := binary.LittleEndian.Uint32(p[i+4 : i+8])
		if count >= 1 && count <= 200 {
			dirOff = i
			break
		}
	}
	if dirOff < 0 {
		return nil
	}

	fileCount := int(binary.LittleEndian.Uint32(p[dirOff+4 : dirOff+8]))
	pos := dirOff + 12 // skip total_size + file_count + reserved

	var files []pendingFile
	for i := 0; i < fileCount; i++ {
		if pos+4 > len(p) {
			break
		}
		fnameLen := int(binary.LittleEndian.Uint32(p[pos : pos+4]))
		pos += 4
		if fnameLen <= 0 || pos+fnameLen > len(p) {
			break
		}
		fname := strings.TrimRight(string(p[pos:pos+fnameLen]), "\x00")
		fname = filepath.Base(fname)
		pos += fnameLen

		if pos+4 > len(p) {
			break
		}
		dataLen := int(binary.LittleEndian.Uint32(p[pos : pos+4]))
		pos += 4
		if dataLen < 0 || pos+dataLen > len(p) {
			break
		}
		data := make([]byte, dataLen)
		copy(data, p[pos:pos+dataLen])
		pos += dataLen

		files = append(files, pendingFile{name: fname, data: data})
	}
	return files
}

// extractZip unpacks a zip archive from p and returns its entries as pendingFiles.
// Returns nil if p is not a valid zip.
func extractZip(p []byte) []pendingFile {
	zr, err := zip.NewReader(bytes.NewReader(p), int64(len(p)))
	if err != nil {
		return nil
	}
	var files []pendingFile
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxUploadSize))
		rc.Close()
		if err != nil {
			continue
		}
		files = append(files, pendingFile{name: filepath.Base(f.Name), data: data})
	}
	return files
}

// extractMultipart parses a multipart/form-data body and returns each file
// part as a pendingFile.
func extractMultipart(r *http.Request) ([]pendingFile, error) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return nil, err
	}
	var files []pendingFile
	for _, headers := range r.MultipartForm.File {
		for _, fh := range headers {
			f, err := fh.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(io.LimitReader(f, maxUploadSize))
			f.Close()
			if err != nil {
				continue
			}
			files = append(files, pendingFile{name: fh.Filename, data: data})
		}
	}
	return files, nil
}

// parseCrashContext fills crash fields from UE's CrashContext.runtime-xml bytes.
func parseCrashContext(data []byte, crash *models.Crash) {
	var ctx crashContextXML
	if err := xml.Unmarshal(data, &ctx); err != nil {
		return
	}
	rp := ctx.RuntimeProperties
	if rp.CrashGUID != "" {
		crash.GUID = rp.CrashGUID
	}
	crash.GameName = rp.GameName
	crash.Platform = rp.PlatformName
	crash.BuildVersion = rp.BuildVersion
	crash.EngineVersion = rp.EngineVersion
	crash.CrashType = rp.CrashType
	crash.ErrorMessage = rp.ErrorMessage
	crash.CallStack = rp.CallStack
	crash.UserDesc = rp.UserDescription
}

// fileNames returns a comma-separated list of names from a pendingFile slice.
func fileNames(files []pendingFile) string {
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.name
	}
	return strings.Join(names, ", ")
}
