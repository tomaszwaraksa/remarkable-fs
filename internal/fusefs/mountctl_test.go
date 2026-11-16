package fusefs

import "testing"

func TestMountFSType(t *testing.T) {
	mountinfo := []byte(`22 1 259:2 / / rw,relatime shared:1 - ext4 /dev/nvme0n1p2 rw
98 22 0:61 / /home/user/remarkable rw,nosuid,nodev,relatime shared:50 - fuse.remarkable-fs remarkable-fs rw,user_id=1000,group_id=1000
99 22 0:62 / /home/user/my\040tablet rw,nosuid,nodev,relatime shared:51 - fuse.remarkable-fs remarkable-fs rw
100 22 0:63 / /mnt/nas rw,relatime shared:52 - nfs4 nas:/export rw
101 22 0:64 / /mnt/nas rw,relatime shared:53 - fuse.sshfs host:/ rw
`)
	cases := []struct {
		path      string
		want      string
		isMounted bool
	}{
		{"/home/user/remarkable", "fuse.remarkable-fs", true},
		{"/home/user/my tablet", "fuse.remarkable-fs", true},
		{"/mnt/nas", "fuse.sshfs", true},
		{"/home/user/other", "", false},
	}
	for _, tc := range cases {
		got, isMounted := mountFSType(mountinfo, tc.path)
		if got != tc.want || isMounted != tc.isMounted {
			t.Errorf("mountFSType(%q) = %q, %v; want %q, %v", tc.path, got, isMounted, tc.want, tc.isMounted)
		}
	}
}

func TestReleaseStaleMountLeavesPlainFolderAlone(t *testing.T) {
	if err := releaseStaleMount(t.TempDir()); err != nil {
		t.Errorf("A plain folder must be accepted: %v", err)
	}
	if err := releaseStaleMount(t.TempDir() + "/missing"); err != nil {
		t.Errorf("A missing folder must be accepted (Mount creates it): %v", err)
	}
}
