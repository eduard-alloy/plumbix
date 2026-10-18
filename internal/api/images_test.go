package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// fakeDockerImages answers image inspects from a table and builds at once.
type fakeDockerImages struct {
	mu     sync.Mutex
	known  map[string]bool
	builds []string
}

func (f *fakeDockerImages) ImageInspect(_ context.Context, ref string, _ ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.known[ref] {
		return client.ImageInspectResult{}, cerrdefs.ErrNotFound
	}
	var r client.ImageInspectResult
	r.ID, r.Created, r.Size = "sha256:abc", "2026-10-18T18:00:00Z", 1000
	return r, nil
}

func (f *fakeDockerImages) ImageBuild(_ context.Context, _ io.Reader, o client.ImageBuildOptions) (client.ImageBuildResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.builds = append(f.builds, o.Tags[0])
	f.known[o.Tags[0]] = true
	return client.ImageBuildResult{Body: io.NopCloser(strings.NewReader(`{"stream":"Step 1/2 : FROM x\n"}` + "\n"))}, nil
}

type imageBody struct {
	Ref     string `json:"ref"`
	Present bool   `json:"present"`
	Shipped bool   `json:"shipped"`
	Status  string `json:"status"`
	UsedBy  []struct {
		Repo      string `json:"repo"`
		Step      string `json:"step"`
		Validator bool   `json:"validator"`
	} `json:"used_by"`
}

// TestImagesListAndShippedBuild (WU-032): the list merges the pipeline's
// images with the shipped ones and says which exist; a shipped image can be
// built and followed, a third-party one cannot.
func TestImagesListAndShippedBuild(t *testing.T) {
	o := loadOpenAPI(t)
	a := newRunAPI(t)
	fake := a.docker
	fake.mu.Lock()
	fake.known["x"] = true
	fake.mu.Unlock()

	rec := a.do(http.MethodGet, "/api/v1/images", "")
	o.check("Images", rec)
	var view struct {
		Version string      `json:"version"`
		Images  []imageBody `json:"images"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s %v", rec.Code, rec.Body.String(), err)
	}
	by := map[string]imageBody{}
	for _, im := range view.Images {
		by[im.Ref] = im
	}
	if view.Version != "dev" || len(view.Images) != 4 {
		t.Fatalf("view = %+v", view)
	}
	if x := by["x"]; !x.Present || x.Shipped || x.Status != "present" || len(x.UsedBy) != 1 || x.UsedBy[0].Step != "test" || x.UsedBy[0].Validator {
		t.Errorf("x = %+v", x)
	}
	if v := by["v"]; v.Present || v.Status != "missing" || len(v.UsedBy) != 1 || !v.UsedBy[0].Validator {
		t.Errorf("v = %+v", v)
	}
	if ai := by["plumbix-ai-validator:dev"]; !ai.Shipped || ai.Status != "missing" || len(ai.UsedBy) != 0 {
		t.Errorf("shipped ai = %+v", ai)
	}

	// Building a third-party image is refused; a shipped one starts and finishes.
	if rec := a.do(http.MethodPost, "/api/v1/images/x/build", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("build x: %d %s", rec.Code, rec.Body.String())
	}
	rec = a.do(http.MethodPost, "/api/v1/images/plumbix-ai-validator/build", "")
	o.check("ImageBuild", rec)
	var b struct {
		ID, Image, Status string
	}
	if json.Unmarshal(rec.Body.Bytes(), &b); rec.Code != http.StatusAccepted || b.Image != "plumbix-ai-validator:dev" || b.ID == "" {
		t.Fatalf("build: %d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		rec = a.do(http.MethodGet, "/api/v1/images/builds/"+b.ID, "")
		o.check("ImageBuild", rec)
		json.Unmarshal(rec.Body.Bytes(), &b)
		if b.Status != "running" || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if b.Status != "succeeded" || !strings.Contains(rec.Body.String(), "Step 1/2") {
		t.Errorf("finished build = %s", rec.Body.String())
	}
	if rec := a.do(http.MethodGet, "/api/v1/images/builds/ib_nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown build: %d", rec.Code)
	}
	// The image now exists and is current.
	rec = a.do(http.MethodGet, "/api/v1/images", "")
	if !strings.Contains(rec.Body.String(), `"ref":"plumbix-ai-validator:dev","present":true`) {
		t.Errorf("after the build: %s", rec.Body.String())
	}
}
