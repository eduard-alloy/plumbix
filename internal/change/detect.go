package change

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	githubSSH = regexp.MustCompile(`^(?:ssh://)?git@github\.com[:/]([A-Za-z0-9-]+/[A-Za-z0-9._-]+)$`)
	azureSSH  = regexp.MustCompile(`^(?:ssh://)?git@ssh\.dev\.azure\.com[:/]v3/([^/\s]+)/([^/\s]+)/([^/\s]+)$`)
	userInfo  = regexp.MustCompile(`^https://[^@/]+@`)
)

// DetectRemote names the host of a git remote and the URL Plumbix registers
// for it (WU-041): GitHub and Azure DevOps HTTPS URLs without user info,
// their SSH forms rewritten to HTTPS so the host token applies, and any
// other SSH, scp-style, file:// or absolute remote as plain git. Other
// HTTPS hosts have no host support and are refused.
func DetectRemote(remote string) (host, url string, err error) {
	r := strings.TrimSpace(remote)
	if m := githubSSH.FindStringSubmatch(r); m != nil {
		return HostGitHub, "https://github.com/" + m[1], nil
	}
	if m := azureSSH.FindStringSubmatch(r); m != nil {
		return HostAzureDevOps, fmt.Sprintf("https://dev.azure.com/%s/%s/_git/%s", m[1], m[2], m[3]), nil
	}
	if strings.HasPrefix(r, "https://") {
		r = strings.TrimRight(userInfo.ReplaceAllString(r, "https://"), "/")
		switch {
		case githubURL.MatchString(r):
			return HostGitHub, r, nil
		case azureURLs[0].MatchString(r) || azureURLs[1].MatchString(r):
			return HostAzureDevOps, r, nil
		}
		return "", "", fmt.Errorf("%s is not a GitHub or Azure DevOps URL; Plumbix reaches other hosts as plain git over SSH", r)
	}
	if sshURL.MatchString(r) || scpURL.MatchString(r) || strings.HasPrefix(r, "file:///") || strings.HasPrefix(r, "/") {
		return HostGit, r, nil
	}
	if r == "" {
		return "", "", fmt.Errorf("the remote is empty")
	}
	return "", "", fmt.Errorf("%s is not a remote Plumbix can reach: use HTTPS for GitHub or Azure DevOps, or ssh://, user@host:path, file:// or an absolute path", r)
}
