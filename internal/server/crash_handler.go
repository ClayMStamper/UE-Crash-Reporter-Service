package server

import (
	"archive/zip"
	"bytes"
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
		pending, err = extractOctetStream(r)
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
		pending, err = extractOctetStream(r)
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

// extractOctetStream reads the body, tries to unzip it, and returns each entry
// as a pendingFile. If the body is not a valid zip it is returned as a single
// raw blob named "raw.bin" so we at least preserve the bytes.
func extractOctetStream(r *http.Request) ([]pendingFile, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(body) == 0 {
		return nil, nil
	}

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		// Not a zip — store the raw bytes anyway.
		return []pendingFile{{name: "raw.bin", data: body}}, nil
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
	return files, nil
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
