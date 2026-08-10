// Package dotenv loads a .env file into the process environment at startup.
//
// It exists because of how this server is launched. An MCP client execs the
// binary directly, so there is no shell to source anything, and the client's own
// env block does not survive an ssh hop. A file sitting next to the binary is
// the one place that works the same on every machine that has a clone of the
// repo — which is also what makes it the natural home for the API key, since
// each machine then keeps its own.
package dotenv

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PathEnv names the file explicitly, for a layout the search below does not
// cover. Set and missing is an error: naming a file is a statement that it
// exists, and falling back silently would hide the typo.
const PathEnv = "HOMELAB_MCP_ENV_FILE"

// Loaded describes what a load did, so the caller can log it. Values are never
// included: this file holds a credential.
type Loaded struct {
	Path    string   // the file that was read; empty when none was found
	Applied []string // names of the variables this file actually set
	Skipped []string // names already present in the environment, left alone
}

// Load finds a .env and applies it. A variable already present in the
// environment always wins: the file is a fallback for a binary started with no
// configuration, not an override of a deliberate one, so `VAR=x ./server`, an
// ssh command prefix and a systemd EnvironmentFile all still take effect.
//
// Finding nothing is not an error. Most installs configure the server some
// other way and never create the file.
func Load() (Loaded, error) {
	if explicit := strings.TrimSpace(os.Getenv(PathEnv)); explicit != "" {
		return loadFile(explicit)
	}

	for _, candidate := range candidatePaths(executableDir(), workingDir()) {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return loadFile(candidate)
		}
	}
	return Loaded{}, nil
}

// candidatePaths is the search order, most specific first.
//
// The working directory is last and cannot be relied on: a client that execs
// this binary sets the cwd to whatever it happens to be, usually not the repo.
// The executable's own directory is the only anchor that holds — and one level
// above it, because `make build` puts the binary in bin/ and the file belongs at
// the root of the repo next to the README that documents it.
func candidatePaths(exeDir, wd string) []string {
	var out []string
	seen := map[string]bool{}

	add := func(dir string) {
		if dir == "" {
			return
		}
		p := filepath.Join(dir, ".env")
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}

	add(exeDir)
	if exeDir != "" {
		add(filepath.Dir(exeDir))
	}
	add(wd)

	return out
}

func executableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	// A binary reached through a symlink (say /usr/local/bin/homelab-mcp) should
	// resolve to where it actually lives, which is where its .env would be.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

func workingDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func loadFile(path string) (Loaded, error) {
	f, err := os.Open(path)
	if err != nil {
		return Loaded{}, fmt.Errorf("reading %s: %w", path, err)
	}
	defer f.Close()

	pairs, err := parse(f)
	if err != nil {
		return Loaded{Path: path}, fmt.Errorf("%s: %w", path, err)
	}

	result := Loaded{Path: path}
	for _, p := range pairs {
		if _, present := os.LookupEnv(p.key); present {
			result.Skipped = append(result.Skipped, p.key)
			continue
		}
		if err := os.Setenv(p.key, p.value); err != nil {
			return result, fmt.Errorf("%s: setting %s: %w", path, p.key, err)
		}
		result.Applied = append(result.Applied, p.key)
	}

	return result, nil
}

// WorldReadable reports whether a file anyone on the host could read holds the
// configuration. It is not enforced — the operator may have reasons — but a
// file with an API key in it that is readable by every account on the machine
// is worth saying out loud once.
func WorldReadable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().Perm()&fs.FileMode(0o077) != 0
}

type pair struct{ key, value string }

// Keys are checked rather than accepted, so a line the shell would also reject
// does not turn into a variable nothing reads.
var validKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parse reads the KEY=VALUE lines, in file order.
//
// A malformed line is an error rather than something to skip: the whole point
// of this file is to carry a key, and quietly ignoring the line that carries it
// produces "radarr is not configured" with nothing pointing at why.
func parse(r io.Reader) ([]pair, error) {
	var out []pair

	scanner := bufio.NewScanner(r)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}

		// `export FOO=bar` is accepted so the same file can be sourced by a
		// shell wrapper without editing.
		if rest, ok := strings.CutPrefix(text, "export "); ok {
			text = strings.TrimSpace(rest)
		}

		key, rawValue, found := strings.Cut(text, "=")
		if !found {
			return nil, fmt.Errorf("line %d is not KEY=VALUE: %q", line, scanner.Text())
		}

		// Cut splits on the FIRST '=' only, so a base64 key ending in '=' or a
		// URL carrying a query string survives intact.
		key = strings.TrimSpace(key)
		if !validKey.MatchString(key) {
			return nil, fmt.Errorf("line %d has an invalid variable name %q", line, key)
		}

		out = append(out, pair{key: key, value: value(rawValue)})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return out, nil
}

// value unquotes and strips a trailing comment, matching what `set -a; . file`
// would do in a shell — the alternative this package replaces, and the one an
// operator's fingers already know.
func value(raw string) string {
	raw = strings.TrimSpace(raw)

	// Quoted: taken literally, which is how a value containing spaces or a '#'
	// is protected. No escape sequences are interpreted.
	if len(raw) >= 2 {
		if q := raw[0]; (q == '"' || q == '\'') && raw[len(raw)-1] == q {
			return raw[1 : len(raw)-1]
		}
	}

	// Unquoted: a '#' after whitespace starts a comment. One without whitespace
	// in front is part of the value, so a URL fragment or a password containing
	// '#' is not silently truncated.
	for i := 1; i < len(raw); i++ {
		if raw[i] == '#' && (raw[i-1] == ' ' || raw[i-1] == '\t') {
			return strings.TrimSpace(raw[:i])
		}
	}

	return raw
}
