package composefile

import (
	"strings"
	"testing"
)

func TestParseSelectsImageServiceWithMapLabel(t *testing.T) {
	file, err := Parse("compose.yml", []byte(`
services:
  app:
    image: example/app:latest
    labels:
      watchlet.enable: "true"
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(file.Services) != 1 {
		t.Fatalf("Services length = %d, want 1", len(file.Services))
	}
	service := file.Services[0]
	if service.ComposePath != "compose.yml" || service.Name != "app" || service.Image != "example/app:latest" {
		t.Fatalf("service = %#v", service)
	}
}

func TestParseSelectsImageServiceWithListLabel(t *testing.T) {
	file, err := Parse("stack.yml", []byte(`
services:
  app:
    image: example/app:latest
    labels:
      - watchlet.enable=true
      - other=value
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(file.Services) != 1 || file.Services[0].Name != "app" {
		t.Fatalf("Services = %#v", file.Services)
	}
}

func TestParseSkipsMissingOrFalseLabels(t *testing.T) {
	file, err := Parse("compose.yml", []byte(`
services:
  unlabeled:
    image: example/unlabeled:latest
  disabled:
    image: example/disabled:latest
    labels:
      watchlet.enable: "false"
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(file.Services) != 0 {
		t.Fatalf("Services = %#v, want none", file.Services)
	}
	if len(file.Skipped) != 2 {
		t.Fatalf("Skipped length = %d, want 2", len(file.Skipped))
	}
	for _, skipped := range file.Skipped {
		if skipped.ComposePath != "compose.yml" || skipped.Reason != SkipUnselected {
			t.Fatalf("skipped = %#v", skipped)
		}
	}
}

func TestParseSkipsSelectedBuildOnlyService(t *testing.T) {
	file, err := Parse("compose.yml", []byte(`
services:
  app:
    build: .
    labels:
      watchlet.enable: "true"
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(file.Services) != 0 {
		t.Fatalf("Services = %#v, want none", file.Services)
	}
	if len(file.Skipped) != 1 || file.Skipped[0].Reason != SkipLocalBuild || file.Skipped[0].Name != "app" {
		t.Fatalf("Skipped = %#v", file.Skipped)
	}
}

func TestParseImageAndBuildClassifiesAsLocalBuildSkip(t *testing.T) {
	file, err := Parse("compose.yml", []byte(`
services:
  app:
    image: example/app:latest
    build: .
    labels:
      watchlet.enable: "true"
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(file.Services) != 0 {
		t.Fatalf("Services = %#v, want none", file.Services)
	}
	if len(file.Skipped) != 1 || file.Skipped[0].Reason != SkipLocalBuild || file.Skipped[0].Name != "app" {
		t.Fatalf("Skipped = %#v", file.Skipped)
	}
	if len(file.Invalid) != 0 {
		t.Fatalf("Invalid = %#v, want none", file.Invalid)
	}
}

func TestParseInvalidSelectedServiceWithoutImageOrBuild(t *testing.T) {
	file, err := Parse("compose.yml", []byte(`
services:
  app:
    labels:
      watchlet.enable: "true"
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(file.Invalid) != 1 {
		t.Fatalf("Invalid length = %d, want 1", len(file.Invalid))
	}
	invalid := file.Invalid[0]
	if invalid.ComposePath != "compose.yml" || invalid.Name != "app" || !strings.Contains(invalid.Reason, "image or build") {
		t.Fatalf("invalid = %#v", invalid)
	}
}

func TestParseMalformedYAMLFailsClearly(t *testing.T) {
	_, err := Parse("bad.yml", []byte("services:\n  app: ["))
	if err == nil || !strings.Contains(err.Error(), "parse compose file") || !strings.Contains(err.Error(), "bad.yml") {
		t.Fatalf("expected clear parse error, got %v", err)
	}
}

func TestParseRejectsNonMapServices(t *testing.T) {
	_, err := Parse("bad.yml", []byte("services: []"))
	if err == nil || !strings.Contains(err.Error(), "services must be a map") {
		t.Fatalf("expected services map error, got %v", err)
	}
}

func TestParseRejectsUnsupportedLabelsShape(t *testing.T) {
	_, err := Parse("bad.yml", []byte(`
services:
  app:
    image: example/app:latest
    labels: 7
`))
	if err == nil || !strings.Contains(err.Error(), "labels must be a map or list") {
		t.Fatalf("expected labels shape error, got %v", err)
	}
}
