package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/mohammadjaf013/weft/core"
)

type stubPlugin struct {
	name   string
	kinds  []string
	err    error
	panic  bool
	assets []core.AssetRef
}

func (p *stubPlugin) Name() string { return p.name }

func (p *stubPlugin) Capabilities() core.Capabilities {
	return core.Capabilities{Name: p.name, SupportedKinds: p.kinds}
}

func (p *stubPlugin) Process(ctx context.Context, in core.TaskInput) (core.TaskOutput, error) {
	if p.panic {
		panic("boom from plugin")
	}
	if p.err != nil {
		return core.TaskOutput{}, p.err
	}
	return core.TaskOutput{Assets: p.assets}, nil
}

func TestRegistryRegisterAndLookup(t *testing.T) {
	r := New()
	thumb := &stubPlugin{name: "thumbnail", kinds: []string{"thumbnail"}}
	if err := r.Register(thumb); err != nil {
		t.Fatal(err)
	}
	if !r.HasPlugin("thumbnail") {
		t.Fatal("plugin not registered by name")
	}
	p, ok := r.PluginFor("thumbnail")
	if !ok || p.Name() != "thumbnail" {
		t.Fatalf("lookup by kind failed: %v %v", p, ok)
	}
	// duplicate name rejected
	if err := r.Register(&stubPlugin{name: "thumbnail", kinds: []string{"x"}}); err == nil {
		t.Fatal("expected duplicate name rejection")
	}
	// kind clash rejected
	if err := r.Register(&stubPlugin{name: "other", kinds: []string{"thumbnail"}}); err == nil {
		t.Fatal("expected kind clash rejection")
	}
}

func TestRegistryProcessUnknownKind(t *testing.T) {
	r := New()
	_, err := r.Process(context.Background(), "nope", core.TaskInput{})
	if !errors.Is(err, ErrNoPluginForKind) {
		t.Fatalf("got %v, want ErrNoPluginForKind", err)
	}
}

func TestRegistryProcessPanicIsContained(t *testing.T) {
	r := New()
	r.Register(&stubPlugin{name: "video", kinds: []string{"video_encode"}, panic: true})

	out, err := r.Process(context.Background(), "video_encode", core.TaskInput{})
	if err == nil {
		t.Fatal("expected error from panicking plugin")
	}
	if !errors.Is(err, ErrPluginPanicked) {
		t.Fatalf("got %v, want ErrPluginPanicked", err)
	}
	if len(out.Assets) != 0 {
		t.Fatal("panicking plugin must not return assets")
	}
}

func TestRegistryProcessError(t *testing.T) {
	r := New()
	r.Register(&stubPlugin{name: "video", kinds: []string{"video_encode"}, err: errors.New("encode failed")})
	_, err := r.Process(context.Background(), "video_encode", core.TaskInput{})
	if err == nil || err.Error() != "encode failed" {
		t.Fatalf("got %v", err)
	}
}

func TestRegistryProcessSuccess(t *testing.T) {
	r := New()
	r.Register(&stubPlugin{name: "thumb", kinds: []string{"thumbnail"}, assets: []core.AssetRef{{Kind: "thumbnail", Name: "t.jpg"}}})
	out, err := r.Process(context.Background(), "thumbnail", core.TaskInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Assets) != 1 || out.Assets[0].Name != "t.jpg" {
		t.Fatalf("assets = %v", out.Assets)
	}
}
