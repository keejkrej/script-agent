package scriptagent

import "github.com/keejkrej/script-agent/internal/artifacts"

// ArtifactInfo describes the embedded QuickPy guest artifact.
type ArtifactInfo struct {
	Name   string `json:"name"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
	ABI    int    `json:"abi"`
	Engine string `json:"engine"`
}

// PythonArtifactInfo returns the embedded QuickPy artifact metadata. It does
// not compile or execute the artifact.
func PythonArtifactInfo() ArtifactInfo {
	m := artifacts.QuickPyMetadata()
	return ArtifactInfo{
		Name:   m.Name,
		Size:   m.Size,
		SHA256: m.SHA256,
		ABI:    m.ABI,
		Engine: m.Engine,
	}
}

// VerifyPythonArtifact checks that the embedded QuickPy bytes match the
// recorded size and SHA-256.
func VerifyPythonArtifact() error { return artifacts.VerifyQuickPy() }
