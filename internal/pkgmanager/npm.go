package pkgmanager

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ossf/package-analysis/internal/utils"
	"github.com/ossf/package-analysis/pkg/api/analysisrun"
	"github.com/ossf/package-analysis/pkg/api/pkgecosystem"
)

// npmPackageJSON represents relevant JSON data from the NPM registry response
// when package information is requested.
// See https://github.com/npm/registry/blob/master/docs/responses/package-metadata.md
type npmPackageJSON struct {
	DistTags struct {
		Latest string `json:"latest"`
	} `json:"dist-tags"`
}

// npmVersionJSON represents relevant JSON data from the NPM registry response
// when package version information is requested.
// See https://github.com/npm/registry/blob/master/docs/responses/package-metadata.md
type npmVersionJSON struct {
	Dist struct {
		Tarball string `json:"tarball"`
	} `json:"dist"`
}

func getNPMLatest(pkg string) (string, error) {
	resp, err := http.Get(fmt.Sprintf("https://registry.npmjs.org/%s", pkg))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	decoder := json.NewDecoder(resp.Body)
	var details npmPackageJSON
	err = decoder.Decode(&details)
	if err != nil {
		return "", err
	}

	return details.DistTags.Latest, nil
}

func getNPMArchiveURL(pkgName, version string) (string, error) {
	resp, err := http.Get(fmt.Sprintf("https://registry.npmjs.org/%s/%s", pkgName, version))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	responseBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading HTTP response: %w", err)
	}

	responseString := string(responseBytes)
	decoder := json.NewDecoder(strings.NewReader(responseString))
	var packageInfo npmVersionJSON
	err = decoder.Decode(&packageInfo)
	if err != nil {
		// invalid version, non-existent package, etc. Details in responseString
		return "", fmt.Errorf("%w. NPM response: %s", err, responseString)
	}

	return packageInfo.Dist.Tarball, nil
}

var npmPkgManager = PkgManager{
	ecosystem:      pkgecosystem.NPM,
	image:          "gcr.io/ossf-malware-analysis/node",
	command:        "/usr/local/bin/analyze.js",
	latestVersion:  getNPMLatest,
	archiveURL:     getNPMArchiveURL,
	extractArchive: utils.ExtractTarGzFile,
	dynamicPhases:  analysisrun.DefaultDynamicPhases(),
}

var npmPkgManagerCombinedSandbox = PkgManager{
	ecosystem:      pkgecosystem.NPM,
	image:          combinedDynamicAnalysisImage,
	command:        "/usr/local/bin/analyze-node.js",
	latestVersion:  getNPMLatest,
	archiveURL:     getNPMArchiveURL,
	extractArchive: utils.ExtractTarGzFile,
	dynamicPhases:  analysisrun.DefaultDynamicPhases(),
}
