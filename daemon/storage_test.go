package daemon

import (
	"testing"

	"github.com/mohammadjaf013/weft/plugins/storage/ssh"
	"github.com/mohammadjaf013/weft/runtime/store/sqlite"
)

func TestBuildStorageSSHFromRegisteredConfig(t *testing.T) {
	st, err := buildStorage("ssh", sqlite.StorageServer{
		ID:   3,
		Type: "ssh",
		Host: "media.example.com",
		User: "deploy",
		Config: map[string]any{
			"key_path":  "C:\\Users\\me\\.ssh\\id_weft",
			"base_path": "/srv/weft",
			"port":      2222,
		},
	}, "")
	if err != nil {
		t.Fatalf("buildStorage(ssh): %v", err)
	}
	if st.Scheme() != "ssh" {
		t.Fatalf("scheme = %q, want ssh", st.Scheme())
	}
}

func TestBuildStorageSSHWithDestPath(t *testing.T) {
	st, err := buildStorage("ssh", sqlite.StorageServer{
		ID:   3,
		Type: "ssh",
		Host: "media.example.com",
		User: "deploy",
		Config: map[string]any{
			"password":  "secret",
			"base_path": "/var/videos",
		},
	}, "series")
	if err != nil {
		t.Fatalf("buildStorage(ssh): %v", err)
	}
	sshSt, ok := st.(*ssh.Storage)
	if !ok {
		t.Fatalf("storage type = %T, want *ssh.Storage", st)
	}
	if got := sshSt.RemotePath("job_1/x.m3u8"); got != "/var/videos/series/job_1/x.m3u8" {
		t.Fatalf("RemotePath = %q, want /var/videos/series/job_1/x.m3u8", got)
	}
}

func TestBuildStorageSSHRequiresKeyPath(t *testing.T) {
	if _, err := buildStorage("ssh", sqlite.StorageServer{
		ID:   4,
		Type: "ssh",
		Host: "media.example.com",
	}, ""); err == nil {
		t.Fatal("buildStorage(ssh) without key_path must error")
	}
}

func TestBuildStorageUnknownType(t *testing.T) {
	if _, err := buildStorage("ftp", sqlite.StorageServer{}, ""); err == nil {
		t.Fatal("buildStorage(ftp) must error")
	}
}
