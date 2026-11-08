package config

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// secretFileRows — the rows that may be supplied through a file instead of the ENV, for Docker secrets, systemd
// credentials, Kubernetes secret mounts and anything else that refuses to put a value in the process
// environment (where `docker inspect` and /proc/<pid>/environ can read it).
//
// Only secrets are listed. A general `<ROW>_FILE` mechanism would let a stale file silently shadow a value
// someone typed into the environment — a debugging trap with no upside for a timeout in seconds.
var secretFileRows = []string{
	"TELEGRAM_BOT_TOKEN",
	"STRATA_API_KEY",
	"WEBHOOK_SECRET_TOKEN",
}

const (
	// secretFileMaxBytes bounds what is read from a secret file. A token or a key is tens of bytes; anything
	// larger is not a secret and must not be pulled into memory on the word of a path in the environment.
	secretFileMaxBytes = 4 << 10
	// secretFileSuffix is the spelling the loader resolves: TELEGRAM_BOT_TOKEN_FILE → TELEGRAM_BOT_TOKEN.
	secretFileSuffix = "_FILE"
)

// ExpandFileRows resolves every listed `<ROW>_FILE` into the row itself and reports what it could not do.
//
// The rule is strict on purpose: the path must be absolute, the file must be readable and non-empty, the
// content must be valid UTF-8 without NUL bytes, and setting both the row and its `_FILE` spelling is a
// configuration problem rather than a precedence question. A bot that starts with a half-read secret is worse
// than one that refuses with the name of the file.
//
// The value is never put into a problem message: only the row name and the reason.
func ExpandFileRows(raw map[string]string, readFile func(string) ([]byte, error)) []string {
	var problems []string
	for _, name := range secretFileRows {
		var path = strings.TrimSpace(raw[name+secretFileSuffix])
		if path == "" {
			continue
		}
		if strings.TrimSpace(raw[name]) != "" {
			problems = Push(problems, fmt.Sprintf("ENV %s and %s are both set: choose one spelling, the value is refused ", name, name+secretFileSuffix))
			continue
		}
		if !strings.HasPrefix(path, "/") && !isWindowsAbsolutePath(path) {
			problems = Push(problems, fmt.Sprintf("ENV %s must be an absolute path to the secret file ", name+secretFileSuffix))
			continue
		}
		var payload, err = readFile(path)
		if err != nil {
			problems = Push(problems, fmt.Sprintf("ENV %s points at a file that cannot be read (%v) ", name+secretFileSuffix, err))
			continue
		}
		if len(payload) > secretFileMaxBytes {
			problems = Push(problems, fmt.Sprintf("ENV %s names a file larger than %d bytes: that is not a secret ", name+secretFileSuffix, secretFileMaxBytes))
			continue
		}
		// Every secret store adds a trailing newline; trimming it is what makes `--secret` and a hand-written
		// file behave the same.
		var value = strings.TrimSpace(string(payload))
		if value == "" {
			problems = Push(problems, fmt.Sprintf("ENV %s names an empty file ", name+secretFileSuffix))
			continue
		}
		if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			problems = Push(problems, fmt.Sprintf("ENV %s names a file with NUL bytes or invalid UTF-8 ", name+secretFileSuffix))
			continue
		}
		raw[name] = value
		delete(raw, name+secretFileSuffix)
	}
	return problems
}

// isWindowsAbsolutePath — `C:\dir\file` or `C:/dir/file`. The loader is also used on Windows, where the secret
// file is a normal file rather than a Docker mount.
func isWindowsAbsolutePath(path string) bool {
	if len(path) < 3 || path[1] != ':' {
		return false
	}
	var drive = path[0]
	if !(drive >= 'A' && drive <= 'Z' || drive >= 'a' && drive <= 'z') {
		return false
	}
	return path[2] == '\\' || path[2] == '/'
}

// readSecretFile is the default reader: the file is opened read-only and read through a bounded reader, so a
// path pointing at a device or a huge file cannot stream forever.
func readSecretFile(path string) ([]byte, error) {
	var file, err = os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, secretFileMaxBytes+1))
}
