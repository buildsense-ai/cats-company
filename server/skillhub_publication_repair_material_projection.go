package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
)

const publicationRepairProjectionMaxBytes = 1024 * 1024

// Pure, internal original-candidate parser, not an authority or validation
// service. A future trusted caller must bracket read/parse with CURRENT task,
// lease and live Runtime source checks. Never accept a browser/model report.
// Supports Runtime's source-only files envelope (text or canonical Base64),
// NOT ZIP, explicit manifest/form, later rounds, execution or filesystem I/O.
func projectPublicationRepairMaterial(ctx context.Context, task store.SkillHubPublicationRepairTask, raw []byte, report PublicationValidationReport) (publicationRepairModelMaterial, error) {
	invalid := store.ErrSkillHubPublicationInvalid
	if err := ctx.Err(); err != nil {
		return publicationRepairModelMaterial{}, err
	}
	if len(task.Rounds) != 1 || task.Rounds[0].Lease.Validate() != nil || !validPublicationRepairModelTask(task, task.Rounds[0].Lease) {
		return publicationRepairModelMaterial{}, invalid
	}
	receipt := task.Input.Receipt
	if receipt.Validate() != nil || len(raw) == 0 || len(raw) > publicationCaptureRuntimeMaxBytes ||
		int64(len(raw)) != receipt.Query.RequestBytes || publicationDescriptionSHA256Hex(string(raw)) != receipt.Query.RequestHash {
		return publicationRepairModelMaterial{}, invalid
	}
	report = snapshotPublicationRepairModelReport(report)
	evidence, err := publicationRepairEvidence(report, receipt.Query.SourceSnapshotHash)
	if err != nil || evidence != task.Input.Evidence {
		return publicationRepairModelMaterial{}, invalid
	}
	selector, err := publicationModelDescriptionProposal(report, "selector eligibility only")
	if err != nil {
		return publicationRepairModelMaterial{}, invalid
	}
	body, err := publicationExactObject(raw, "source")
	if err != nil {
		return publicationRepairModelMaterial{}, invalid
	}
	source, err := publicationExactObject(body["source"], "type", "files")
	if err != nil || string(bytes.TrimSpace(source["type"])) != `"files"` {
		return publicationRepairModelMaterial{}, invalid
	}
	d := json.NewDecoder(bytes.NewReader(source["files"]))
	if token, err := d.Token(); err != nil || token != json.Delim('[') {
		return publicationRepairModelMaterial{}, invalid
	}
	type fileText struct {
		EntryIndex int    `json:"entryIndex"`
		Path       string `json:"path"`
		Content    string `json:"content"`
	}
	files := make([]fileText, 0)
	facts := make(map[string]string)
	total := 0
	for d.More() {
		if ctx.Err() != nil {
			return publicationRepairModelMaterial{}, ctx.Err()
		}
		if len(files) >= 500 {
			return publicationRepairModelMaterial{}, invalid
		}
		var entry json.RawMessage
		if d.Decode(&entry) != nil {
			return publicationRepairModelMaterial{}, invalid
		}
		fields, err := publicationExactObject(entry, "path", "contentBase64")
		encoded := err == nil
		if !encoded {
			fields, err = publicationExactObject(entry, "path", "content")
		}
		if err != nil {
			return publicationRepairModelMaterial{}, invalid
		}
		p, err := publicationMaterialString(fields["path"])
		if err != nil || !publicationMaterialPath(p) {
			return publicationRepairModelMaterial{}, invalid
		}
		if _, exists := facts[p]; exists {
			return publicationRepairModelMaterial{}, invalid
		}
		key := "content"
		if encoded {
			key = "contentBase64"
		}
		text, err := publicationMaterialString(fields[key])
		if err != nil {
			return publicationRepairModelMaterial{}, invalid
		}
		var content []byte
		if encoded {
			if len(text) > base64.StdEncoding.EncodedLen(publicationRepairProjectionMaxBytes) {
				return publicationRepairModelMaterial{}, invalid
			}
			content, err = base64.StdEncoding.Strict().DecodeString(text)
			if err != nil || base64.StdEncoding.EncodeToString(content) != text {
				return publicationRepairModelMaterial{}, invalid
			}
		} else {
			content = []byte(text)
		}
		total += len(content)
		if total > publicationRepairProjectionMaxBytes || !utf8.Valid(content) || strings.ContainsFunc(string(content), func(r rune) bool {
			return (r < 32 && r != '\n' && r != '\r' && r != '\t') || r == 127
		}) {
			return publicationRepairModelMaterial{}, invalid
		}
		// Exact Cloud canonical JSON: UTF-8 bytewise path sort, SHA256 bytes,
		// empty explicit manifest, no newline or Go HTML/U+2028 escaping.
		facts[p] = `{"path":` + cloudDescriptionJSONString(p) + `,"sha256":"` + publicationDescriptionSHA256Hex(string(content)) + `","size":` + strconv.Itoa(len(content)) + `}`
		files = append(files, fileText{len(files), p, string(content)})
	}
	if token, err := d.Token(); err != nil || token != json.Delim(']') || len(files) == 0 {
		return publicationRepairModelMaterial{}, invalid
	}
	if _, err := d.Token(); err != io.EOF {
		return publicationRepairModelMaterial{}, invalid
	}
	if _, exists := facts["SKILL.md"]; !exists {
		return publicationRepairModelMaterial{}, invalid
	}
	paths := make([]string, 0, len(facts))
	for p := range facts {
		parts := strings.Split(p, "/")
		for i := 1; i < len(parts); i++ {
			if _, exists := facts[strings.Join(parts[:i], "/")]; exists {
				return publicationRepairModelMaterial{}, invalid
			}
		}
		paths = append(paths, p)
	}
	sort.Strings(paths) // Go bytewise UTF-8 order matches Cloud Buffer.compare.
	canonicalFacts := make([]string, 0, len(paths))
	for _, p := range paths {
		canonicalFacts = append(canonicalFacts, facts[p])
	}
	canonical := `{"domain":"skillhub-publish-candidate-v1","files":[` + strings.Join(canonicalFacts, ",") + `],"manifest":{}}`
	if publicationDescriptionSHA256Hex(canonical) != receipt.Query.SourceSnapshotHash {
		return publicationRepairModelMaterial{}, invalid
	}
	// Preserve source order: Cloud report entryIndex refers to that order, not
	// the sorted hash order. The model cannot select/rebind the target file.
	if selector.EntryIndex != nil {
		i := *selector.EntryIndex
		if i < 0 || i >= int64(len(files)) || files[i].Path != selector.RelativePath {
			return publicationRepairModelMaterial{}, invalid
		}
	} else if _, exists := facts[selector.RelativePath]; !exists {
		return publicationRepairModelMaterial{}, invalid
	}
	projected, err := json.Marshal(struct {
		SchemaVersion int        `json:"schemaVersion"`
		Kind          string     `json:"kind"`
		CandidateHash string     `json:"candidateHash"`
		Files         []fileText `json:"files"`
	}{1, "untrusted-skill-source", receipt.Query.SourceSnapshotHash, files})
	if err != nil || len(projected) > publicationRepairProjectionMaxBytes {
		return publicationRepairModelMaterial{}, invalid
	}
	if err := ctx.Err(); err != nil {
		return publicationRepairModelMaterial{}, err
	}
	return publicationRepairModelMaterial{SourceText: string(projected), Report: report}, nil
}

func publicationMaterialPath(p string) bool {
	if p == "" || len(utf16.Encode([]rune(p))) > 180 || strings.HasPrefix(p, "~") || strings.ContainsAny(p, "\\") ||
		(len(p) >= 2 && ((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z')) && p[1] == ':') ||
		strings.ContainsFunc(p, func(r rune) bool { return r < 32 || r == 127 }) {
		return false
	}
	parts := strings.Split(p, "/")
	if len(parts) > 12 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// encoding/json silently replaces unpaired UTF-16 surrogates with U+FFFD.
// Reject those escapes before decoding; legitimate literal U+FFFD is data.
func publicationMaterialString(raw []byte) (string, error) {
	raw = bytes.TrimSpace(raw)
	invalid := store.ErrSkillHubPublicationInvalid
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' || !utf8.Valid(raw) {
		return "", invalid
	}
	for i := 1; i < len(raw)-1; i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw)-1 {
			return "", invalid
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw)-1 {
			return "", invalid
		}
		v, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return "", invalid
		}
		i += 4
		if v >= 0xdc00 && v <= 0xdfff {
			return "", invalid
		}
		if v >= 0xd800 && v <= 0xdbff {
			if i+6 >= len(raw)-1 || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return "", invalid
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return "", invalid
			}
			i += 6
		}
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", invalid
	}
	return value, nil
}
