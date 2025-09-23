package schema

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const (
	schemasDir = "schemas"
)

// ResolveRemoteRefs looks for $ref keys inside schema blocks and, if the URI
// is an http(s) URL it fetches the remote schema, saves it locally and update
// the URI to the new file
func ResolveRemoteRefs(r *bytes.Reader, chartBasePath string) ([]byte, error) {
	schemas := filepath.Join(chartBasePath, schemasDir)
	if err := os.MkdirAll(schemas, 0755); err != nil {
		return nil, err
	}

	var (
		out bytes.Buffer
		buf = make([]byte, 0, 64*1024) // 64k buffer for the scanner
		re  = regexp.MustCompile(`(?m)^\s*#\s*@schema\s*`)
	)
	out.Grow(r.Len()) // pre-size output

	scanner := bufio.NewScanner(r)
	scanner.Buffer(buf, 1024*1024)

	var inSchema bool

	for scanner.Scan() {
		line := scanner.Bytes()

		// Detect if we are inside a schema block
		if re.Match(line) {
			inSchema = !inSchema
			// Add line to result as-is
			out.Write(line)
			out.WriteByte('\n')
			continue
		}

		// Skip all comments that does not contain $ref
		idx := bytes.Index(line, []byte("$ref:"))
		if idx == -1 {
			// Add line to result as-is
			out.Write(line)
			out.WriteByte('\n')
			continue
		}

		// Extract URL part (after "$ref:" and any following whitespace)
		urlBytes := bytes.TrimSpace(line[idx+5:])
		u, err := url.Parse(string(urlBytes))
		if err != nil {
			return nil, fmt.Errorf("invalid url %q: %w", urlBytes, err)
		}
		base := filepath.Base(u.Path)
		local := filepath.Join(schemas, base)

		if err := fetchRemoteRef(local, string(urlBytes)); err != nil {
			return nil, fmt.Errorf("fetch %s: %w", urlBytes, err)
		}

		out.Write(line[:idx])
		// Change ref to local file and append to result
		out.WriteString("$ref: file://")
		out.WriteString(filepath.ToSlash(filepath.Join(schemasDir, base)))
		out.WriteByte('\n')
	}
	return out.Bytes(), scanner.Err()
}

func fetchRemoteRef(dst, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return nil // ignore non-remote refs
	}

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	// copy up to 8 MiB
	_, err = io.Copy(f, io.LimitReader(resp.Body, 8<<20))
	return err
}
