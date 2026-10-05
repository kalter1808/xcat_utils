package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"xcat-ports/internal/noderange"
)

// syncEntry represents a single mapping in an xCAT synclist file.
type syncEntry struct {
	sources []string
	target  string
	nodes   []string // nil means applies to all nodes
}

// parseSynclistFile parses an xCAT synclist file.
// Format of lines:
//
//	/path/src1 [/path/src2 ...] -> [/path/target | /path/target/ | (noderange) /path/target]
//
// Lines starting with # and blank lines are ignored.
// Sections such as EXECUTE:, EXECUTEALWAYS:, APPEND:, MERGE: are recognized.
func parseSynclistFile(filePath string) ([]syncEntry, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("cannot open synclist file: %w", err)
	}
	defer f.Close()

	var entries []syncEntry
	scanner := bufio.NewScanner(f)
	lineNum := 0
	inSpecialClause := false

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Check for section headers
		upper := strings.ToUpper(line)
		if upper == "EXECUTE:" || upper == "EXECUTEALWAYS:" || upper == "APPEND:" || upper == "MERGE:" {
			inSpecialClause = true
			continue
		}

		// In APPEND or MERGE clauses, lines may still follow src -> dest
		if !strings.Contains(line, "->") {
			// Lines without -> inside EXECUTE/EXECUTEALWAYS are script names, skip in standalone mode
			if inSpecialClause {
				continue
			}
			continue
		}

		parts := strings.SplitN(line, "->", 2)
		if len(parts) != 2 {
			continue
		}

		left := strings.TrimSpace(parts[0])
		right := strings.TrimSpace(parts[1])

		// Parse source paths (can have wildcards)
		srcTokens := strings.Fields(left)
		var sources []string
		for _, tok := range srcTokens {
			if strings.ContainsAny(tok, "*?[]") {
				matches, err := filepath.Glob(tok)
				if err == nil && len(matches) > 0 {
					sources = append(sources, matches...)
					continue
				}
			}
			sources = append(sources, tok)
		}

		if len(sources) == 0 {
			continue
		}

		// Parse target and optional node filter: (noderange) /dest/path
		var targetNodes []string
		if strings.HasPrefix(right, "(") {
			closeIdx := strings.Index(right, ")")
			if closeIdx > 0 {
				nrExpr := right[1:closeIdx]
				right = strings.TrimSpace(right[closeIdx+1:])
				matched, _ := noderange.Expand(nrExpr)
				targetNodes = matched
			}
		}

		target := right
		// Strip any inline comments on the right side if preceded by whitespace and #
		if idx := strings.Index(target, " #"); idx >= 0 {
			target = strings.TrimSpace(target[:idx])
		} else if idx := strings.Index(target, "\t#"); idx >= 0 {
			target = strings.TrimSpace(target[:idx])
		}

		entries = append(entries, syncEntry{
			sources: sources,
			target:  target,
			nodes:   targetNodes,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading synclist file %s: %w", filePath, err)
	}

	return entries, nil
}

// filterSyncEntriesForNode filters synclist entries for a specific target node.
func filterSyncEntriesForNode(entries []syncEntry, node string) []syncEntry {
	var filtered []syncEntry
	for _, e := range entries {
		if len(e.nodes) == 0 {
			filtered = append(filtered, e)
			continue
		}
		for _, n := range e.nodes {
			if n == node {
				filtered = append(filtered, e)
				break
			}
		}
	}
	return filtered
}

// copyLocalToImage syncs entries locally into an install rootimg directory.
func copyLocalToImage(imagePath string, entries []syncEntry, trace bool) error {
	fi, err := os.Stat(imagePath)
	if err != nil || !fi.IsDir() {
		return fmt.Errorf("input image directory %s does not exist or is not a directory", imagePath)
	}

	for _, entry := range entries {
		cleanTarget := strings.TrimPrefix(entry.target, "/")
		destPath := filepath.Join(imagePath, cleanTarget)

		isDirDest := strings.HasSuffix(entry.target, "/") || len(entry.sources) > 1
		if isDirDest {
			if err := os.MkdirAll(destPath, 0o755); err != nil {
				return fmt.Errorf("cannot create destination directory %s: %w", destPath, err)
			}
			for _, src := range entry.sources {
				dstFile := filepath.Join(destPath, filepath.Base(src))
				if trace {
					fmt.Printf("copying %s to %s\n", src, dstFile)
				}
				if err := copyFile(src, dstFile); err != nil {
					return err
				}
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return fmt.Errorf("cannot create destination directory for %s: %w", destPath, err)
			}
			if trace {
				fmt.Printf("copying %s to %s\n", entry.sources[0], destPath)
			}
			if err := copyFile(entry.sources[0], destPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyFile copies a single file preserving permissions and timestamps.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("cannot open source file %s: %w", src, err)
	}
	defer in.Close()

	fi, err := in.Stat()
	if err != nil {
		return fmt.Errorf("cannot stat source file %s: %w", src, err)
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return fmt.Errorf("cannot create target file %s: %w", dst, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("cannot copy data from %s to %s: %w", src, dst, err)
	}

	_ = os.Chtimes(dst, fi.ModTime(), fi.ModTime())
	return nil
}
