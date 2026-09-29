package main

import (
	"os"
	"os/exec"
	"regexp"
	"sort"
)

var (
	hostUpRe     = regexp.MustCompile(`Host (.*) \(.*\) appears to be up`)
	nmapReportRe = regexp.MustCompile(`Nmap scan report for ([^ ]*) `)
)

// regexpStripHF strips a trailing "-hf<n>" suffix (pping line 141).
var regexpStripHF = stripHF{}

type stripHF struct{}

func (stripHF) ReplaceStringString(s string) string {
	return stripHFRe.ReplaceAllString(s, "")
}

var stripHFRe = regexp.MustCompile(`-hf\d$`)

func executableExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

func executableInPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
