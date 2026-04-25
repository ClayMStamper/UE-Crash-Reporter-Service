package server

import (
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
		CrashGUID     string `xml:"CrashGUID"`
		GameName      string `xml:"GameName"`
		PlatformName  string `xml:"PlatformName"`
		BuildVersion  string `xml:"BuildVersion"`
		EngineVersion string `xml:"EngineVersion"`
		CrashType     string `xml:"Misc.CrashType"`
		ErrorMessage  string `xml:"ErrorMessage"`
		CallStack     string `xml:"CallStack"`
		UserDescription string `xml:"UserDescription"`
	} `xml:"RuntimeProperties"`
}

const maxUploadSize = 128 << 20 // 128 MB

// receiveCrash handles POST /api/v1/crash from UE's CrashReportClient.
//
// UE sends a multipart/form-data body containing one or more files:
//   - CrashContext.runtime-xml  — structured crash metadata
//   - <GameName>.log            — game log
//   - <hash>.dmp               — minidump
//   - Diagnostics.txt          — system summary
//
// Any number/combination of files is accepted. We parse what we can.
func (s *Server) receiveCrash(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		s.log.Printf("parse multipart: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	crash := &models.Crash{
		ReceivedAt: time.Now().UTC(),
		GUID:       fmt.Sprintf("manual-%d", time.Now().UnixNano()), // fallback GUID
	}

	// Collect all uploaded files and look for CrashContext.
	type pendingFile struct {
		name string
		data []byte
	}
	var pending []pendingFile

	for fieldName, headers := range r.MultipartForm.File {
		_ = fieldName
		for _, fh := range headers {
			f, err := fh.Open()
			if err != nil {
				s.log.Printf("open upload %s: %v", fh.Filename, err)
				continue
			}
			data, err := io.ReadAll(io.LimitReader(f, maxUploadSize))
			f.Close()
			if err != nil {
				s.log.Printf("read upload %s: %v", fh.Filename, err)
				continue
			}
			pending = append(pending, pendingFile{name: fh.Filename, data: data})
		}
	}

	// Also allow a raw body with a single file (some UE versions do this).
	if len(pending) == 0 && r.ContentLength > 0 {
		data, err := io.ReadAll(io.LimitReader(r.Body, maxUploadSize))
		if err == nil && len(data) > 0 {
			pending = append(pending, pendingFile{name: "raw.bin", data: data})
		}
	}

	// Parse CrashContext.runtime-xml first.
	for _, pf := range pending {
		if strings.EqualFold(pf.name, "crashcontext.runtime-xml") {
			parseCrashContext(pf.data, crash)
		}
	}

	if len(pending) == 0 {
		s.log.Printf("received empty crash report — ignoring")
		w.WriteHeader(http.StatusOK)
		return
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
