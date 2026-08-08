package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	cfg "github.com/mohammadjaf013/weft/configs"
	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/storage/local"
	"github.com/mohammadjaf013/weft/plugins/storage/s3"
	"github.com/mohammadjaf013/weft/plugins/storage/ssh"
	"github.com/mohammadjaf013/weft/runtime/store/sqlite"
)

// toStoreWebhook converts a config webhook into a store Webhook row.
func toStoreWebhook(id string, w cfg.WebhookCfg) sqlite.Webhook {
	return sqlite.Webhook{
		ID:             id,
		URL:            w.URL,
		Secret:         w.Secret,
		Events:         w.Events,
		MaxRetries:     w.MaxRetries,
		TimeoutSeconds: w.TimeoutSeconds,
		CreatedAt:      core.Now(),
	}
}

// buildStorage constructs a core.Storage from a registered storage-server row.
// destPath is the job-level subdirectory appended under the server's base path.
// Config keys are storage-specific; unknown types are rejected up front so a
// misconfigured destination fails the job, never the whole daemon.
func buildStorage(kind string, sv sqlite.StorageServer, destPath string) (core.Storage, error) {
	m := sv.Config
	join := func(root string) string {
		if root == "" {
			root = "./data"
		}
		if destPath == "" {
			return root
		}
		return filepath.ToSlash(filepath.Join(root, destPath))
	}
	switch kind {
	case "local":
		base, _ := m["base_path"].(string)
		return local.New(join(base))
	case "ssh":
		host := sv.Host
		user := sv.User
		if user == "" {
			user = "weft"
		}
		keyPath, _ := m["key_path"].(string)
		password, _ := m["password"].(string)
		if keyPath == "" && password == "" {
			return nil, fmt.Errorf("ssh destination %d requires config key_path or password", sv.ID)
		}
		port := 22
		if p, ok := m["port"].(float64); ok && int(p) != 0 {
			port = int(p)
		}
		return ssh.New(ssh.Config{
			Host: host, User: user, KeyPath: keyPath, Password: password, Port: port,
			BasePath: join(orStr(m, "base_path", "")),
		})
	case "s3", "minio", "r2":
		return s3.New(s3.Config{
			Endpoint:  sv.Host,
			Bucket:    orStr(m, "bucket", ""),
			Region:    orStr(m, "region", "us-east-1"),
			AccessKey: orStr(m, "access_key", ""),
			SecretKey: orStr(m, "secret_key", ""),
			BasePath:  join(orStr(m, "base_path", "")),
		})
	default:
		return nil, fmt.Errorf("unknown storage type %q", kind)
	}
}

func orStr(m map[string]any, k, def string) string {
	if v, ok := m[k].(string); ok && v != "" {
		return v
	}
	return def
}

var _ = context.Background
var _ = time.Now
