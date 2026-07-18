package release

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Component struct {
	Name            string
	Version         string
	SPDXID          string
	LicenseDeclared string
	Download        string
	SHA256          string
}

type SBOMOptions struct {
	Name       string
	Namespace  string
	Created    time.Time
	Creator    string
	Components []Component
}

type spdxDocument struct {
	SPDXVersion       string             `json:"spdxVersion"`
	DataLicense       string             `json:"dataLicense"`
	SPDXID            string             `json:"SPDXID"`
	Name              string             `json:"name"`
	DocumentNamespace string             `json:"documentNamespace"`
	CreationInfo      spdxCreation       `json:"creationInfo"`
	Packages          []spdxPackage      `json:"packages"`
	Relationships     []spdxRelationship `json:"relationships"`
}

type spdxCreation struct {
	Created  string   `json:"created"`
	Creators []string `json:"creators"`
}

type spdxPackage struct {
	Name             string         `json:"name"`
	SPDXID           string         `json:"SPDXID"`
	VersionInfo      string         `json:"versionInfo"`
	DownloadLocation string         `json:"downloadLocation"`
	FilesAnalyzed    bool           `json:"filesAnalyzed"`
	LicenseConcluded string         `json:"licenseConcluded"`
	LicenseDeclared  string         `json:"licenseDeclared"`
	Checksums        []spdxChecksum `json:"checksums,omitempty"`
}

type spdxChecksum struct {
	Algorithm     string `json:"algorithm"`
	ChecksumValue string `json:"checksumValue"`
}

type spdxRelationship struct {
	SPDXElementID      string `json:"spdxElementId"`
	RelationshipType   string `json:"relationshipType"`
	RelatedSPDXElement string `json:"relatedSpdxElement"`
}

// WriteSPDX emits canonical SPDX 2.3 JSON with stable package and relationship
// ordering. The namespace and creation time must be supplied by release policy.
func WriteSPDX(writer io.Writer, options SBOMOptions) error {
	if !validComponent(options.Name) || !validNamespace(options.Namespace) || !reasonableEpoch(options.Created) || !validCreator(options.Creator) || len(options.Components) == 0 || len(options.Components) > maxComponents {
		return ErrInvalidInput
	}
	components := append([]Component(nil), options.Components...)
	sort.Slice(components, func(i, j int) bool { return components[i].SPDXID < components[j].SPDXID })
	packages := make([]spdxPackage, 0, len(components))
	relationships := make([]spdxRelationship, 0, len(components))
	seen := make(map[string]struct{}, len(components))
	for _, component := range components {
		if !validComponent(component.Name) || !validVersion(component.Version) || !validSPDXID(component.SPDXID) || !validSPDXExpression(component.LicenseDeclared) || !validDownload(component.Download) {
			return ErrInvalidInput
		}
		if _, duplicate := seen[component.SPDXID]; duplicate {
			return ErrInvalidInput
		}
		seen[component.SPDXID] = struct{}{}
		checksums := []spdxChecksum(nil)
		if component.SHA256 != "" {
			if !validDigest(component.SHA256) {
				return ErrInvalidInput
			}
			checksums = []spdxChecksum{{Algorithm: "SHA256", ChecksumValue: component.SHA256}}
		}
		packages = append(packages, spdxPackage{
			Name: component.Name, SPDXID: component.SPDXID, VersionInfo: component.Version,
			DownloadLocation: component.Download, FilesAnalyzed: false,
			LicenseConcluded: "NOASSERTION", LicenseDeclared: component.LicenseDeclared, Checksums: checksums,
		})
		relationships = append(relationships, spdxRelationship{SPDXElementID: "SPDXRef-DOCUMENT", RelationshipType: "DESCRIBES", RelatedSPDXElement: component.SPDXID})
	}
	document := spdxDocument{
		SPDXVersion: "SPDX-2.3", DataLicense: "CC0-1.0", SPDXID: "SPDXRef-DOCUMENT",
		Name: options.Name, DocumentNamespace: options.Namespace,
		CreationInfo:  spdxCreation{Created: options.Created.Format(time.RFC3339), Creators: []string{options.Creator}},
		Packages:      packages,
		Relationships: relationships,
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("%w: encode SBOM", ErrIO)
	}
	if _, err := writer.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("%w: write SBOM", ErrIO)
	}
	return nil
}

func validSPDXID(value string) bool {
	return strings.HasPrefix(value, "SPDXRef-") && validToken(strings.TrimPrefix(value, "SPDXRef-"), 128, ".-")
}

func validNamespace(value string) bool {
	return len(value) >= 8 && len(value) <= 512 && validPublicURL(value, "https")
}

func validCreator(value string) bool {
	prefix, name, found := strings.Cut(value, ": ")
	return found && (prefix == "Tool" || prefix == "Organization" || prefix == "Person") && validComponent(name)
}

func validDownload(value string) bool {
	return value == "NOASSERTION" || len(value) <= 512 && (validPublicURL(value, "https") || validPublicURL(value, "git+https"))
}

func validPublicURL(value, scheme string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == scheme && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && !strings.ContainsAny(value, " \x00\r\n\t")
}
