// Command release runs the release workflow's checks.
//
//	go run ./scripts/release check <tag>   refuse a tag that disagrees with internal/version
//	go run ./scripts/release notes         print the changelog section for internal/version
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/evanwtf/llm-metrics-exporter/internal/release"
	"github.com/evanwtf/llm-metrics-exporter/internal/version"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if len(os.Args) < 2 {
		log.Error("usage: release check <tag> | release notes")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "check":
		if len(os.Args) != 3 {
			log.Error("usage: release check <tag>")
			os.Exit(2)
		}
		if err := release.CheckTag(os.Args[2], version.Version); err != nil {
			log.Error("refusing to release", "err", err)
			os.Exit(1)
		}
		log.Info("tag matches the declared version", "tag", os.Args[2])
	case "notes":
		b, err := os.ReadFile("docs/changelog.md")
		if err == nil {
			var notes string
			if notes, err = release.Notes(string(b), version.Version); err == nil {
				// The notes are this command's output, for --notes-file.
				fmt.Print(notes)
				return
			}
		}
		log.Error("refusing to release", "err", err)
		os.Exit(1)
	default:
		log.Error("unknown command", "command", os.Args[1])
		os.Exit(2)
	}
}
