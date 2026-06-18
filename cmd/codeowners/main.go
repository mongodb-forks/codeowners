package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/hmarr/codeowners"
	flag "github.com/spf13/pflag"
)

func main() {
	var (
		ownerFilters   []string
		showUnowned    bool
		codeownersPath string
		trackedOnly    bool
		helpFlag       bool
	)
	flag.StringSliceVarP(&ownerFilters, "owner", "o", nil, "filter results by owner")
	flag.BoolVarP(&showUnowned, "unowned", "u", false, "only show unowned files (can be combined with -o)")
	flag.StringVarP(&codeownersPath, "file", "f", "", "CODEOWNERS file path")
	flag.BoolVarP(&trackedOnly, "tracked", "t", false, "only show files tracked by git")
	flag.BoolVarP(&helpFlag, "help", "h", false, "show this help message")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: codeowners <path>...\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if helpFlag {
		flag.Usage()
		os.Exit(0)
	}

	var trackedFiles map[string]bool
	if trackedOnly {
		trackedFiles = getTrackedFiles()
	}

	ruleset, err := loadCodeowners(codeownersPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	paths := flag.Args()
	if len(paths) == 0 {
		paths = append(paths, ".")
	}

	// Make the @ optional for GitHub teams and usernames
	for i := range ownerFilters {
		ownerFilters[i] = strings.TrimLeft(ownerFilters[i], "@")
	}

	// Gather the paths to match first so the matching work, which is CPU-bound,
	// can be spread across a pool of worker goroutines below.
	var filePaths []string
	for _, startPath := range paths {
		// godirwalk only accepts directories, so we need to handle files separately
		if !isDir(startPath) {
			filePaths = append(filePaths, startPath)
			continue
		}

		err = filepath.WalkDir(startPath, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if path == ".git" {
				return filepath.SkipDir
			}

			// Only show code owners for files, not directories
			if !d.IsDir() {
				if trackedOnly {
					if _, ok := trackedFiles[path]; !ok {
						return nil
					}
				}
				filePaths = append(filePaths, path)
			}
			return nil
		})

		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v", err)
			os.Exit(1)
		}
	}

	// Match each path against the ruleset in parallel, preserving input order
	// for deterministic output. Matching is the dominant, CPU-bound cost.
	results := make([]string, len(filePaths))
	var matchErr error
	var errOnce sync.Once

	workers := runtime.NumCPU()
	if workers > len(filePaths) {
		workers = len(filePaths)
	}

	var wg sync.WaitGroup
	var next int64 = -1
	index := func() int { return int(atomic.AddInt64(&next, 1)) }

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := index()
				if i >= len(filePaths) {
					return
				}
				line, err := fileOwnersLine(ruleset, filePaths[i], ownerFilters, showUnowned)
				if err != nil {
					errOnce.Do(func() { matchErr = err })
					return
				}
				results[i] = line
			}
		}()
	}
	wg.Wait()

	if matchErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v", matchErr)
		os.Exit(1)
	}

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	for _, line := range results {
		if line != "" {
			out.WriteString(line)
		}
	}
}

// fileOwnersLine returns the formatted output line for a single path (including
// its trailing newline), or an empty string if the path should not be shown
// given the current filters.
func fileOwnersLine(
	ruleset codeowners.Ruleset,
	path string,
	ownerFilters []string,
	showUnowned bool,
) (string, error) {
	rule, err := ruleset.Match(path)
	if err != nil {
		return "", err
	}
	// If we didn't get a match, the file is unowned
	if rule == nil || rule.Owners == nil {
		// Unless explicitly requested, don't show unowned files if we're filtering by owner
		if len(ownerFilters) == 0 || showUnowned {
			return fmt.Sprintf("%-70s  (unowned)\n", path), nil
		}
		return "", nil
	}

	// Figure out which of the owners we need to show according to the --owner filters
	ownersToShow := make([]string, 0, len(rule.Owners))
	for _, o := range rule.Owners {
		// If there are no filters, show all owners
		filterMatch := len(ownerFilters) == 0 && !showUnowned
		for _, filter := range ownerFilters {
			if filter == o.Value {
				filterMatch = true
			}
		}
		if filterMatch {
			ownersToShow = append(ownersToShow, o.String())
		}
	}

	// If the owners slice is empty, no owners matched the filters so don't show anything
	if len(ownersToShow) > 0 {
		return fmt.Sprintf("%-70s  %s\n", path, strings.Join(ownersToShow, " ")), nil
	}
	return "", nil
}

func loadCodeowners(path string) (codeowners.Ruleset, error) {
	if path == "" {
		return codeowners.LoadFileFromStandardLocation()
	}
	return codeowners.LoadFile(path)
}

// isDir checks if there's a directory at the path specified.
func isDir(path string) bool {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	}
	return info.IsDir()
}

func getTrackedFiles() map[string]bool {
	// Ensure the script is run inside a Git repository
	if _, err := os.Stat(".git"); os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "error: this is not a Git repository.")
		os.Exit(1)
	}

	cmd := exec.Command("git", "ls-files")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "Error running git ls-files:", err)
		os.Exit(1)
	}

	var trackedFiles = make(map[string]bool)
	files := strings.Split(out.String(), "\n")
	for _, file := range files {
		if file != "" {
			trackedFiles[file] = true
		}
	}

	return trackedFiles
}
