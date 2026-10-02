package api

import "testing"

func TestValidateTemplate(t *testing.T) {
	cases := []struct {
		fst, src string
		ok       bool
	}{
		{"nfs", "lumpy.local:/mnt/nvme/mm/mount1", true},
		{"nfs4", "10.0.0.5:/export", true},
		{"nfs", "lumpy.local", false},
		{"nfs", ":/export", false},
		{"nfs", "/local/path", false},
		{"wekafs", "weka01/fs1", true},
		{"wekafs", "weka01,weka02/fs1", true},
		{"wekafs", "weka01", false},
		{"wekafs", "weka01/", false},
		{"wekafs", "/fs1", false},
		{"ext4", "/dev/sda1", false},
		{"xfs", "x", false},
		{"cifs", "//srv/share", false},
		{"fuse.vault-fs", "vault01:/projects", true},
		{"fuse.vault-fs", "/vault/projects", true},
		{"fuse.vault-fs", "vault://cluster/projects", true},
		{"fuse.vault-fs", "projects", true},
		{"fuse.vault-fs", "", false},
		{"fuse.vault-fs", "   ", false},
		{"fuse.vault-fs", "two words", false},
		{"fuse.sshfs", "user@host:/path", false}, // other FUSE filesystems are not supported implicitly
		{"fuse", "x", false},
		{"fuse.vault", "x", false},
	}
	for _, c := range cases {
		if got := ValidateTemplate(c.fst, c.src) == ""; got != c.ok {
			t.Errorf("ValidateTemplate(%q,%q) ok=%v want %v", c.fst, c.src, got, c.ok)
		}
	}
}
