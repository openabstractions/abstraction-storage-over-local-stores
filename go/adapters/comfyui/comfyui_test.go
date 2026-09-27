package comfyui_test

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/comfyui"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

const amd = "AppData/Local/AMD/AI_Bundle/ComfyUI/ComfyUI"

func TestDiscoveryReadsExtraModelPaths(t *testing.T) {
	home := fixture.Home(t, "comfyui")
	d := comfyui.New().Discover(fixture.Env(home, nil))
	l := fixture.Location(t, d, "comfyui")
	if !l.Present || !fixture.SamePath(l.Locator, filepath.Join(home, filepath.FromSlash(amd), "models")) || !strings.Contains(l.Rule, "AMD") {
		t.Fatalf("location %+v", l)
	}
	var got []string
	for _, c := range d.Claims {
		if c.Holder.Program != "comfyui" || c.Holder.Instance != "" || c.Store != "comfyui-extra:a111" || !fixture.SamePath(c.Base, filepath.Join(home, "stable-diffusion-webui")) {
			t.Fatalf("claim %+v", c)
		}
		if !strings.HasSuffix(filepath.ToSlash(c.Basis.Index), "extra_model_paths.yaml") {
			t.Fatalf("claim index %s", c.Basis.Index)
		}
		rel, _ := filepath.Rel(c.Base, c.Path)
		got = append(got, c.Basis.Key+" "+filepath.ToSlash(rel))
	}
	sort.Strings(got)
	fixture.Equal(t, "claims", got, []string{
		"a111.checkpoints models/Stable-diffusion",
		"a111.loras models/Lora",
		"a111.loras models/LyCORIS",
		"a111.vae models/VAE",
	})
}

func TestConfiguredInstall(t *testing.T) {
	home := fixture.Home(t, "comfyui-malformed")
	env := fixture.Env(home, nil)
	env.Config.ComfyUI = []string{filepath.Join(home, "ComfyUI")}
	o := fixture.Observe(t, comfyui.New(), env, "comfyui")
	if !strings.Contains(o.Rule, "operator") {
		t.Fatalf("rule %s", o.Rule)
	}
	fixture.Equal(t, "errors", fixture.ErrorKinds(o), []string{"malformed_index " + filepath.Join(home, "ComfyUI", "extra_model_paths.yaml")})
	fixture.MustManifest(t, o, "comfyui", "checkpoints/a.safetensors")
}

func TestEmptyAndMissing(t *testing.T) {
	fixture.EmptyAndMissing(t, comfyui.New(), "comfyui", amd+"/models", "checkpoints")
}

func TestModelsTree(t *testing.T) {
	home := fixture.Home(t, "comfyui")
	o := fixture.Observe(t, comfyui.New(), fixture.Env(home, nil), "comfyui")
	if len(o.Errors) != 0 {
		t.Fatalf("errors %v", o.Errors)
	}
	fixture.Equal(t, "names", fixture.Names(o), []string{
		"comfyui:checkpoints/sd15.safetensors",
		"comfyui:text_encoders/umt5_xxl_fp16.safetensors",
		"comfyui:text_encoders/umt5_xxl_fp8_e4m3fn_scaled.safetensors",
	})
	fixture.Equal(t, "holders", fixture.Holders(fixture.MustManifest(t, o, "comfyui", "checkpoints/sd15.safetensors")), []string{"comfyui"})
	part, ok := fixture.Object(o, "checkpoints/SDXL/sd_xl_base_1.0.safetensors.part")
	if !ok || !part.Partial || part.Size != 1500 {
		t.Fatalf("partial %+v", part)
	}
	if len(o.Objects) != 4 || o.OtherFiles != 1 {
		t.Fatalf("objects %v other %d", fixture.Locators(o), o.OtherFiles)
	}
	for _, m := range o.Manifests {
		for _, l := range source.EntryLocators(m.Manifest) {
			if strings.HasSuffix(l, ".part") {
				t.Fatalf("partial file in a manifest")
			}
		}
	}
}
