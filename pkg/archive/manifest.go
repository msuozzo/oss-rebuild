// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/pkg/errors"
)

// Implements MANIFEST.MF spec: https://docs.oracle.com/javase/8/docs/technotes/guides/jar/jar.html#JARManifest

// Section represents a section in the manifest file
type Section struct {
	// Attributes maintains the mapping of names to values for quick lookup
	Attributes map[string]string
	// Order maintains the original order of attributes
	Order []string
}

// NewSection creates a new section
func NewSection() *Section {
	return &Section{
		Attributes: make(map[string]string),
		Order:      make([]string, 0),
	}
}

// Set adds or updates an attribute while maintaining order
func (s *Section) Set(name, value string) {
	if _, exists := s.Attributes[name]; !exists {
		s.Order = append(s.Order, name)
	}
	s.Attributes[name] = value
}

// Get retrieves an attribute value
func (s *Section) Get(name string) (string, bool) {
	v, ok := s.Attributes[name]
	return v, ok
}

// Delete removes an attribute
func (s *Section) Delete(name string) {
	if _, ok := s.Attributes[name]; !ok {
		return
	}
	delete(s.Attributes, name)
	for i, n := range s.Order {
		if n == name {
			s.Order = append(s.Order[:i], s.Order[i+1:]...)
			break
		}
	}
}

// Manifest represents a parsed MANIFEST.MF file
type Manifest struct {
	MainSection     *Section
	EntrySections   []*Section
	OriginalContent []byte // Keep original content for modification
}

// NewManifest creates a new empty manifest
func NewManifest() *Manifest {
	return &Manifest{
		MainSection:   NewSection(),
		EntrySections: make([]*Section, 0),
	}
}

// ParseManifest parses a manifest file from a reader
func ParseManifest(r io.Reader) (*Manifest, error) {
	content, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}

	content = normalizeLineEndings(content)

	manifest := NewManifest()
	manifest.OriginalContent = content
	reader := bufio.NewReader(bytes.NewReader(content))

	currentSection := manifest.MainSection
	var currentLine, continuationLine string
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, errors.Wrap(err, "reading line")
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, " ") {
			// Continuation line
			if currentLine == "" {
				return nil, errors.New("unexpected continuation line")
			}
			continuationLine += strings.TrimPrefix(line, " ")
			continue
		}
		currentLine += continuationLine
		continuationLine = ""
		if err := processManifestLine(currentSection, currentLine); err != nil {
			return nil, err
		}
		currentLine = line
		if line == "" {
			// Section separator
			if currentSection != manifest.MainSection && len(currentSection.Order) > 0 {
				manifest.EntrySections = append(manifest.EntrySections, currentSection)
			}
			currentSection = NewSection()
			if err == io.EOF {
				break
			}
		} else if err == io.EOF {
			return nil, errors.New("missing trailing newline")
		}
	}
	return manifest, nil
}

// processManifestLine processes a single manifest line and adds it to the section
func processManifestLine(section *Section, line string) error {
	if line == "" {
		return nil
	}
	colonIdx := strings.Index(line, ":")
	if colonIdx == -1 {
		return fmt.Errorf("invalid manifest line (missing colon): %s", line)
	}
	name := strings.TrimSpace(line[:colonIdx])
	value := strings.TrimPrefix(line[colonIdx+1:], " ")
	if err := validateName(name); err != nil {
		return fmt.Errorf("invalid name '%s': %w", name, err)
	}
	if _, exists := section.Get(name); exists {
		return fmt.Errorf("duplicate attribute: %s", name)
	}
	section.Set(name, value)
	return nil
}

// validateName checks if a manifest attribute name is valid
func validateName(name string) error {
	if len(name) == 0 {
		return errors.New("empty name")
	}
	for _, c := range name {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_') {
			return errors.Errorf("invalid character in name: %c", c)
		}
	}
	// Check for "From" prefix since someone might think this is an email???
	if strings.HasPrefix(strings.ToLower(name), "from") {
		return errors.New("name cannot start with 'From'")
	}

	return nil
}

// WriteManifest writes a manifest back to a writer
func WriteManifest(w io.Writer, m *Manifest) error {
	if err := writeSection(w, m.MainSection); err != nil {
		return err
	}
	for _, section := range m.EntrySections {
		if _, err := w.Write([]byte("\r\n")); err != nil {
			return err
		}
		if err := writeSection(w, section); err != nil {
			return err
		}
	}
	_, err := w.Write([]byte("\r\n"))
	return err
}

// writeSection writes a single section to a writer
func writeSection(w io.Writer, section *Section) error {
	for _, name := range section.Order {
		value, _ := section.Get(name)
		line := fmt.Sprintf("%s: %s\r\n", name, value)
		// Handle line length restrictions
		if len(line) > 72 {
			parts := splitLine(line)
			for _, part := range parts {
				if _, err := w.Write([]byte(part)); err != nil {
					return err
				}
			}
		} else {
			if _, err := w.Write([]byte(line)); err != nil {
				return err
			}
		}
	}
	return nil
}

// splitLine splits a line longer than 72 bytes into continuation lines
func splitLine(line string) []string {
	var lines []string
	remaining := line
	for len(remaining) > 72 {
		// Find last space before 72 bytes
		splitIdx := 71
		for splitIdx > 0 && remaining[splitIdx] != ' ' {
			splitIdx--
		}
		if splitIdx == 0 {
			// No space found, force split at 71
			splitIdx = 71
		}
		lines = append(lines, remaining[:splitIdx+1]+"\r\n")
		remaining = " " + remaining[splitIdx+1:]
	}
	if remaining != "" {
		lines = append(lines, remaining)
	}
	return lines
}

// normalizeLineEndings ensures consistent CRLF line endings
func normalizeLineEndings(data []byte) []byte {
	// Replace Windows style (CRLF) with Unix style (LF)
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	// Replace Mac style (CR) with Unix style (LF)
	data = bytes.ReplaceAll(data, []byte("\r"), []byte("\n"))
	// Replace Unix style (LF) with Windows style (CRLF)
	data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
	return data
}
