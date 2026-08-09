// Package posterupload implements the poster_upload task: replace a
// published video's poster image with a caller-supplied image file, without
// running ffmpeg at all — the image IS the input (resolved through the exact
// same local/http(s)/source-server InputResolver every other task uses),
// just copied straight to the destination poster path.
package posterupload

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
)

type Plugin struct{}

func (p *Plugin) Name() string { return "poster_upload" }

func (p *Plugin) Capabilities() core.Capabilities {
	return core.Capabilities{
		Name:            "poster_upload",
		SupportedInputs: []string{"jpg", "jpeg", "png", "webp"},
		SupportedKinds:  []string{"poster_upload"},
		EstimatedCPU:    0.05,
		EstimatedRAMMB:  32,
	}
}

// Process copies the resolved input image straight to the destination's
// poster path — no ffmpeg, no work dir; the caller's image IS the poster.
func (p *Plugin) Process(ctx context.Context, in core.TaskInput) (core.TaskOutput, error) {
	if in.InputURI == "" {
		return core.TaskOutput{}, fmt.Errorf("poster_upload: no input resolved for %s", in.InputRef)
	}
	if in.Storage == nil {
		return core.TaskOutput{}, fmt.Errorf("poster_upload: task %s has no destination storage", in.TaskID)
	}
	local := strings.TrimPrefix(in.InputURI, "local:")
	f, err := os.Open(local)
	if err != nil {
		return core.TaskOutput{}, fmt.Errorf("poster_upload: open %s: %w", local, err)
	}
	defer f.Close()

	base := mediautil.BaseName(in)
	if n, _ := in.Params["name"].(string); n != "" {
		base = n
	}
	ext := strings.ToLower(filepath.Ext(local))
	if ext == "" {
		ext = ".jpg"
	}
	name := base + "_poster" + ext
	// Storage.Save resolves purely from Name/URI — Dir is metadata the CALLER
	// folds into the path (see plugins/upload's identical convention), the
	// backends themselves never read it.
	rel := "thumbnails/" + name
	saveRef := core.AssetRef{Kind: "thumbnail", Name: rel, URI: in.Storage.Scheme() + "://" + rel}
	if err := in.Storage.Save(ctx, saveRef, f); err != nil {
		return core.TaskOutput{}, fmt.Errorf("poster_upload: save %s: %w", name, err)
	}
	reported := core.AssetRef{Kind: "thumbnail", Name: name, URI: saveRef.URI, Dir: "thumbnails"}
	if in.Progress != nil {
		in.Progress(100)
	}
	return core.TaskOutput{Assets: []core.AssetRef{reported}}, nil
}
