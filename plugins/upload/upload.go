// Package upload implements the upload task: copy the assets a pipeline
// produced (listed in Params["assets"]) to the job's destination storage.
// The worker/daemon resolves the destination Storage and hands it in via
// TaskInput.Storage before Process is called.
package upload

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mohammadjaf013/weft/core"
)

type Plugin struct{}

func (p *Plugin) Name() string { return "upload" }

func (p *Plugin) Capabilities() core.Capabilities {
	return core.Capabilities{
		Name:            "upload",
		SupportedInputs: []string{"mp4", "mkv", "mov", "ts", "m3u8", "srt", "vtt", "jpg", "png", "webp"},
		SupportedKinds:  []string{"upload"},
		EstimatedCPU:    0.1,
		EstimatedRAMMB:  256,
	}
}

// Process copies each local asset listed in Params["assets"] to in.Storage.
// Asset URIs are "local:<absolute path>" — we open them directly. Destination
// refs are named by their producer-set Dir/Name only; the job's DestPath is
// already folded into the storage root by the daemon (buildStorage), so the
// folder the operator gave on the CLI is the literal final location.
func (p *Plugin) Process(ctx context.Context, in core.TaskInput) (core.TaskOutput, error) {
	assets, _ := in.Params["assets"].([]core.AssetRef)
	if len(assets) == 0 {
		return core.TaskOutput{}, fmt.Errorf("upload: no assets in Params[assets]")
	}
	if in.Storage == nil {
		return core.TaskOutput{}, fmt.Errorf("upload: task %s has no destination storage", in.TaskID)
	}

	uploaded := make([]core.AssetRef, 0, len(assets))
	for i, a := range assets {
		local := strings.TrimPrefix(a.URI, "local:")
		f, err := os.Open(local)
		if err != nil {
			return core.TaskOutput{}, fmt.Errorf("upload: open %s: %w", local, err)
		}
		// Destination path: <Dir>/<name> when the producer set a subdirectory
		// (thumbnails/, subtitle/<lang>/, audio/), <name> otherwise. The job's
		// DestPath is part of the storage root already set by the daemon.
		rel := a.Name
		if a.Dir != "" {
			rel = strings.Trim(a.Dir, "/") + "/" + a.Name
		}
		dst := core.AssetRef{
			Kind:  a.Kind,
			Name:  rel,
			URI:   in.Storage.Scheme() + "://" + rel,
			Bytes: 0,
		}
		n, cerr := copyCounting(ctx, f, dst, in.Storage)
		f.Close()
		if cerr != nil {
			return core.TaskOutput{}, fmt.Errorf("upload %s: %w", a.Name, cerr)
		}
		dst.Bytes = n
		uploaded = append(uploaded, dst)
		if in.Progress != nil {
			in.Progress(float64(i+1) / float64(len(assets)) * 100)
		}
	}
	return core.TaskOutput{Assets: uploaded}, nil
}

func copyCounting(ctx context.Context, r io.Reader, dst core.AssetRef, st core.Storage) (int64, error) {
	cw := &countingWriter{}
	err := st.Save(ctx, dst, io.TeeReader(r, cw))
	return cw.n, err
}

type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }
