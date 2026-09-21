package cfgtpl

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
)

// FilePrefix is what MikroDash names the files Config Management creates on a
// router, so a run that died mid-flight is swept by prefix.
//
// NOT backups.FilePrefix: each sweep deletes by its own prefix, and a shared
// one would let a Backups sweep delete a file an import is about to read.
const FilePrefix = "mikrodash-cfg-"

var (
	ourFile = regexp.MustCompile(`^mikrodash-cfg-[0-9a-f]{16}(\.rsc|-report\.txt|\.backup)$`)
	ourBase = regexp.MustCompile(`^mikrodash-cfg-[0-9a-f]{16}$`)
)

// NewFileName names one file to import: `mikrodash-cfg-<16 hex>.rsc`.
//
// ── NEVER `.auto.rsc` ───────────────────────────────────────────────────────
//
// RouterOS runs a file with `.auto.rsc` in its name by itself, as soon as it is
// uploaded over FTP or SFTP (the manual's Configuration Management page, "Auto
// Import"). MikroDash writes through /file, not FTP, but a name that is never
// one of those is not left to depend on that. This name cannot be: the hex is
// the only variable part, and it holds no dot. TestFileNamesNeverAutoRun holds
// it.
func NewFileName() (string, error) {
	base, err := NewBaseName()
	if err != nil {
		return "", err
	}
	return base + ".rsc", nil
}

// NewBaseName is `mikrodash-cfg-<16 hex>` with no extension: the name the
// dead-man's backup is saved under (RouterOS adds `.backup`) and its scheduler
// is given, so the sweep finds both.
func NewBaseName() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return FilePrefix + hex.EncodeToString(b), nil
}

// ReportName is where a dry-run's report goes for the file being imported.
// `/execute … file=` is given `write`, and RouterOS appends `.txt`, so the
// report is read back as `read` (measured: `mdprobe-out` became
// `mdprobe-out.txt`).
func ReportName(file string) (write, read string) {
	base := file
	if len(base) > 4 && base[len(base)-4:] == ".rsc" {
		base = base[:len(base)-4]
	}
	return base + "-report", base + "-report.txt"
}

// IsOurFile reports whether a router file is one Config Management made, and
// may be swept. The whole shape, not only the prefix: a file an operator
// happened to name `mikrodash-cfg-notes.txt` is theirs.
func IsOurFile(name string) bool { return ourFile.MatchString(name) }

// IsOurScheduler reports whether a scheduler entry is a dead-man Config
// Management armed. Swept with the files: one left by a process that died
// would otherwise fire for ever once its backup file was gone.
func IsOurScheduler(name string) bool { return ourBase.MatchString(name) }
