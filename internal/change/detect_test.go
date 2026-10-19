package change

import "testing"

func TestDetectRemote(t *testing.T) {
	cases := []struct{ in, host, url string }{
		{"https://github.com/eduard-alloy/plumbix.git", HostGitHub, "https://github.com/eduard-alloy/plumbix.git"},
		{"https://github.com/eduard-alloy/plumbix/", HostGitHub, "https://github.com/eduard-alloy/plumbix"},
		{"https://someone@github.com/eduard-alloy/plumbix", HostGitHub, "https://github.com/eduard-alloy/plumbix"},
		{"git@github.com:eduard-alloy/plumbix.git", HostGitHub, "https://github.com/eduard-alloy/plumbix.git"},
		{"ssh://git@github.com/eduard-alloy/plumbix.git", HostGitHub, "https://github.com/eduard-alloy/plumbix.git"},
		{"https://acme@dev.azure.com/acme/ledger/_git/ledger-api", HostAzureDevOps, "https://dev.azure.com/acme/ledger/_git/ledger-api"},
		{"https://acme.visualstudio.com/ledger/_git/ledger-api", HostAzureDevOps, "https://acme.visualstudio.com/ledger/_git/ledger-api"},
		{"git@ssh.dev.azure.com:v3/acme/ledger/ledger-api", HostAzureDevOps, "https://dev.azure.com/acme/ledger/_git/ledger-api"},
		{"ssh://git@git.example.com/team/app.git", HostGit, "ssh://git@git.example.com/team/app.git"},
		{"git@git.example.com:team/app.git", HostGit, "git@git.example.com:team/app.git"},
		{"/srv/git/app.git", HostGit, "/srv/git/app.git"},
		{"file:///srv/git/app.git", HostGit, "file:///srv/git/app.git"},
	}
	for _, c := range cases {
		host, url, err := DetectRemote(c.in)
		if err != nil || host != c.host || url != c.url {
			t.Errorf("DetectRemote(%q) = %q %q %v, want %q %q", c.in, host, url, err, c.host, c.url)
		}
	}
	for _, bad := range []string{"", "https://gitlab.com/team/app.git", "relative/path", "ftp://x/y"} {
		if _, _, err := DetectRemote(bad); err == nil {
			t.Errorf("DetectRemote(%q) accepted", bad)
		}
	}
}

// TestDetectRemoteRefusesMalformedSSH keeps the cases web/src/vm/remote.test.ts shares.
func TestDetectRemoteRefusesMalformedSSH(t *testing.T) {
	for _, bad := range []string{"ssh://host", "git@host:/abs"} {
		if _, _, err := DetectRemote(bad); err == nil {
			t.Errorf("DetectRemote(%q) accepted", bad)
		}
	}
}
