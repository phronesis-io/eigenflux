package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

func Init(logDir string) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		log.Fatalf("failed to create log directory %s: %v", logDir, err)
	}

	filename := time.Now().Format("20060102_150405") + ".log"
	path := filepath.Join(logDir, filename)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Fatalf("failed to open log file %s: %v", path, err)
	}

	w := io.MultiWriter(os.Stdout, f)
	log.SetOutput(w)
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	log.Printf("logging to %s", path)
	fmt.Println()
}
