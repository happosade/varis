package web

import (
	"encoding/base64"
	"net/http"

	"varis/internal/db"

	qrcode "github.com/skip2/go-qrcode"
)

type coverData struct {
	Disk      db.Disk
	QRDataURI string
}

func (s *Server) cover(w http.ResponseWriter, r *http.Request) {
	diskID := r.PathValue("diskID")
	disk, err := db.GetDisk(r.Context(), s.pool, diskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	png, err := qrcode.Encode("archive://disk/"+diskID, qrcode.Medium, 256)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "cover", coverData{Disk: disk, QRDataURI: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)})
}
