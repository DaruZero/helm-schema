package schema

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/DaruZero/helm-schema/pkg/util"
	"github.com/dadav/go-jsonpointer"
	log "github.com/sirupsen/logrus"
)

const (
	schemasDir = "schemas"
)

// handleLocalRefs processes and resolves JSON Schema references ($ref) within a schema.
// It handles both direct and nested schema references.
// It will keep the original $comment and definition if they were set
// For each reference:
// - If it's a relative file path, it attempts to load and parse the referenced schema
// - If it includes a JSON pointer (#/path/to/schema), it extracts the specific schema section
// - The resolved schema replaces the original reference
//
// Parameters:
//   - schema: Pointer to the Schema object containing the references to resolve
//   - valuesPath: Path to the current values file, used for resolving relative paths
//
// The function will log.Fatal on any critical errors (file not found, invalid JSON, etc.)
// and log.Debug for non-critical issues (e.g., non-relative paths that may be handled elsewhere)
func handleLocalRefs(schema *Schema, valuesPath string) {
	originalSchema := *schema
	// Handle main schema $ref
	if schema.Ref != "" {
		refParts := strings.Split(schema.Ref, "#")
		if relFilePath, err := util.IsRelativeFile(valuesPath, refParts[0]); err == nil {
			var relSchema Schema
			file, err := os.Open(relFilePath)
			if err == nil {
				defer file.Close()
				byteValue, _ := io.ReadAll(file)

				if len(refParts) > 1 {
					// Found json-pointer
					var obj interface{}
					json.Unmarshal(byteValue, &obj)
					jsonPointerResultRaw, err := jsonpointer.Get(obj, refParts[1])
					if err != nil {
						log.Fatal(err)
					}
					jsonPointerResultMarshaled, err := json.Marshal(jsonPointerResultRaw)
					if err != nil {
						log.Fatal(err)
					}
					err = json.Unmarshal(jsonPointerResultMarshaled, &relSchema)
					if err != nil {
						log.Fatal(err)
					}
				} else {
					// No json-pointer
					err = json.Unmarshal(byteValue, &relSchema)
					if err != nil {
						log.Fatal(err)
					}
				}
				*schema = relSchema
				schema.HasData = true
				// keep original comment and description if existing
				if originalSchema.Description != "" {
					schema.Description = originalSchema.Description
				}
				if originalSchema.Comment != "" {
					schema.Comment = originalSchema.Comment
				}
			} else {
				log.Fatal(err)
			}
		} else {
			log.Debug(err)
		}
	}

	// Handle $ref in pattern properties
	if schema.PatternProperties != nil {
		for pattern, subSchema := range schema.PatternProperties {
			if subSchema.Ref != "" {
				handleLocalRefs(subSchema, valuesPath)
				schema.PatternProperties[pattern] = subSchema // Update the original schema in the map
			}
		}
	}

	if schema.Items != nil && schema.Items.Ref != "" {
		handleLocalRefs(schema.Items, valuesPath)
	}

	if len(schema.AllOf) > 0 {
		for pattern, subSchema := range schema.AllOf {
			if subSchema.Ref != "" {
				handleLocalRefs(subSchema, valuesPath)
				schema.AllOf[pattern] = subSchema // Update the original schema in the map
			}
		}
	}
	if len(schema.AnyOf) > 0 {
		for pattern, subSchema := range schema.AnyOf {
			if subSchema.Ref != "" {
				handleLocalRefs(subSchema, valuesPath)
				schema.AnyOf[pattern] = subSchema // Update the original schema in the map
			}
		}
	}
	if len(schema.OneOf) > 0 {
		for pattern, subSchema := range schema.OneOf {
			if subSchema.Ref != "" {
				handleLocalRefs(subSchema, valuesPath)
				schema.OneOf[pattern] = subSchema // Update the original schema in the map
			}
		}
	}
}

// resolveRemoteRefs looks for $ref keys inside schema blocks and, if the URI
// is an http(s) URL it fetches the remote schema, saves it locally and update
// the URI to the new file
func resolveRemoteRefs(r *bytes.Reader, chartBasePath string) ([]byte, error) {
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
