// age-github command is a wrapper to filippo.io/age tool which expands
// recipients in -r @username format to first ssh key of github user
// "username", fetching keys from https://github.com/username.keys endpoint.
//
// It caches keys for 1 hour in "age-github" subdirectory under os.UserCacheDir
// directory.
//
// Github user handles should have @ prefix, i.e. to encrypt file for
// https://github.com/artyom user, you call it as
//
//	age-github -r @artyom ...
//
// All other flags/arguments are passed unmodified.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}

func run(args []string) error {
	ctx := context.Background()
	if len(args) == 0 {
		return errors.New(usage)
	}
	ageBin, err := exec.LookPath("age")
	if err != nil {
		return err
	}
	var cache cacheDir
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		cache = cacheDir(filepath.Join(dir, "age-github"))
	}
	ageArgs := make([]string, 0, len(args)+1)
	ageArgs = append(ageArgs, ageBin) // exec needs this
	for i, v := range args {
		if strings.HasPrefix(v, "@") && i > 0 && isRecipientFlag(args[i-1]) {
			userName := v[1:]
			keys, err := fetchGithubKeys(ctx, userName, cache)
			if err != nil {
				return fmt.Errorf("fetching keys for github user %q: %w", userName, err)
			}
			if len(keys) == 0 {
				return fmt.Errorf("no keys found for github user %q", userName)
			}
			ageArgs = append(ageArgs, keys[0])
			continue
		}
		if j := strings.IndexRune(v, '='); j > 0 && isRecipientFlag(v[:j]) {
			flagArg := v[j+1:]
			if !strings.HasPrefix(flagArg, "@") {
				ageArgs = append(ageArgs, v)
				continue
			}
			userName := flagArg[1:]
			keys, err := fetchGithubKeys(ctx, userName, cache)
			if err != nil {
				return fmt.Errorf("fetching keys for github user %q: %w", userName, err)
			}
			if len(keys) == 0 {
				return fmt.Errorf("no keys found for github user %q", userName)
			}
			ageArgs = append(ageArgs, "-r", keys[0])
			continue
		}
		ageArgs = append(ageArgs, v)
	}
	return syscall.Exec(ageBin, ageArgs, os.Environ())
}

func fetchGithubKeys(ctx context.Context, username string, cache cacheDir) ([]string, error) {
	if !validGithubHandle(username) {
		return nil, errors.New("not a valid github user name")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if data, err := cache.get(username); err == nil {
		return parseReaderToKeys(bytes.NewReader(data))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://github.com/"+username+".keys", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "github.com/artyom/age-github")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected response code %q", resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		return nil, fmt.Errorf("unexpected content type %q", ct)
	}
	buf := new(bytes.Buffer) // copy of resp.Body consumed by parseReaderToKeys
	keys, err := parseReaderToKeys(io.TeeReader(io.LimitReader(resp.Body, 1<<18), buf))
	if err != nil {
		return nil, err
	}
	_ = cache.put(username, buf.Bytes())
	return keys, nil
}

// parseReaderToKeys parses reader, returning at most 10 lines starting with
// "ssh-" prefix
func parseReaderToKeys(r io.Reader) ([]string, error) {
	var out []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if len(out) == 10 {
			return out, nil
		}
		line := scanner.Text()
		if strings.HasPrefix(line, "ssh-") {
			out = append(out, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func validGithubHandle(s string) bool {
	return userNameRe.MatchString(s)
}

func isRecipientFlag(s string) bool {
	switch s {
	case "-r", "--r", "-recipient", "--recipient":
		return true
	}
	return false
}

var userNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]+$`)

type cacheDir string

func (c cacheDir) get(key string) ([]byte, error) {
	if c == "" {
		return nil, os.ErrNotExist
	}
	filename := filepath.Join(string(c), fmt.Sprintf("%x", sha1.Sum([]byte(key))))
	st, err := os.Stat(filename)
	if err != nil {
		return nil, err
	}
	if st.ModTime().Add(time.Hour).Before(time.Now()) { // stale entry
		return nil, os.ErrNotExist
	}
	return ioutil.ReadFile(filename)
}

func (c cacheDir) put(key string, data []byte) error {
	if c == "" {
		return nil
	}
	filename := fmt.Sprintf("%x", sha1.Sum([]byte(key)))
	if err := os.MkdirAll(string(c), 0777); err != nil {
		return err
	}
	return ioutil.WriteFile(filepath.Join(string(c), filename), data, 0666)
}

const usage = `age-github is the age tool [1] wrapper which allows using github
user handles as -r flag recipients. This wrapper automatically fetches first ssh
key for a given user from github and calls age with -r flag holding ssh key value.

Github user handles should have @ prefix, i.e. to encrypt file for
https://github.com/artyom user, you call it as

	age-github -r @artyom ...

[1]: https://filippo.io/age`
