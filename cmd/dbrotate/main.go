// Command dbrotate keeps the newest timestamped bot backups. It is a dry-run unless -apply is explicit,
// touches only the generated bot-YYYYMMDD-HHMMSS.db names, and never removes the last retained copy.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
)

var backupName = regexp.MustCompile(`^bot-[0-9]{8}-[0-9]{6}\.db$`)

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "dbrotate: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	var directory = flag.String("dir", "", "backup directory (scripts/db_rotate.ps1 supplies a restricted path)")
	var keep = flag.Int("keep", 7, "how many newest timestamped backups to retain (minimum 1)")
	var apply = flag.Bool("apply", false, "actually delete old backups; without this flag the command only prints the plan")
	flag.Parse()
	if *directory == "" {
		die("-dir is required")
	}
	if err := run(*directory, *keep, *apply, os.Stdout); err != nil {
		die("%s", err)
	}
}

func run(directory string, keep int, apply bool, output io.Writer) error {
	if keep < 1 {
		return fmt.Errorf("keep must be at least 1, got %d", keep)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("cannot read backup directory %q: %w", directory, err)
	}

	names := []string{}
	for _, entry := range entries {
		if entry.IsDir() || !backupName.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("cannot inspect backup candidate %q: %w", entry.Name(), err)
		}
		if info.Mode().IsRegular() {
			names = append(names, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))

	mode := "dry-run"
	if apply {
		mode = "apply"
	}
	fmt.Fprintf(output, "rotation: directory=%s mode=%s found=%d keep=%d candidates=%d\n", directory, mode, len(names), keep, max(0, len(names)-keep))
	for index, name := range names {
		if index < keep {
			fmt.Fprintf(output, "keep: %s\n", name)
			continue
		}
		path := directory + string(os.PathSeparator) + name
		if !apply {
			fmt.Fprintf(output, "would delete: %s\n", name)
			continue
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("cannot delete old backup %q: %w", path, err)
		}
		fmt.Fprintf(output, "deleted: %s\n", name)
	}
	return nil
}
