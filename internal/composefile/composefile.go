package composefile

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

const EnableLabel = "watchlet.enable"

type File struct {
	Path     string
	Services []Service
	Skipped  []SkippedService
	Invalid  []InvalidService
}

type Service struct {
	ComposePath string
	Name        string
	Image       string
}

type SkippedService struct {
	ComposePath string
	Name        string
	Reason      SkipReason
}

type InvalidService struct {
	ComposePath string
	Name        string
	Reason      string
}

type SkipReason string

const (
	SkipUnselected     SkipReason = "not-selected"
	SkipLocalBuild     SkipReason = "local-build"
	SkipInvalidService SkipReason = "invalid-selected-service"
)

type composeDocument struct {
	Services serviceMap `yaml:"services"`
}

type serviceMap map[string]serviceDocument

type serviceDocument struct {
	Image  string      `yaml:"image"`
	Build  any         `yaml:"build"`
	Labels labelsValue `yaml:"labels"`
}

type labelsValue map[string]string

func Load(path string) (File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, fmt.Errorf("read compose file %q: %w", path, err)
	}
	return Parse(path, data)
}

func Parse(path string, data []byte) (File, error) {
	var doc composeDocument
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return File{}, fmt.Errorf("parse compose file %q: %w", path, err)
	}
	if doc.Services == nil {
		return File{}, fmt.Errorf("parse compose file %q: services must be a map", path)
	}

	file := File{Path: path}
	for name, service := range doc.Services {
		if service.Labels[EnableLabel] != "true" {
			file.Skipped = append(file.Skipped, SkippedService{
				ComposePath: path,
				Name:        name,
				Reason:      SkipUnselected,
			})
			continue
		}

		if service.Build != nil {
			file.Skipped = append(file.Skipped, SkippedService{
				ComposePath: path,
				Name:        name,
				Reason:      SkipLocalBuild,
			})
			continue
		}

		if service.Image != "" {
			file.Services = append(file.Services, Service{
				ComposePath: path,
				Name:        name,
				Image:       service.Image,
			})
			continue
		}

		file.Invalid = append(file.Invalid, InvalidService{
			ComposePath: path,
			Name:        name,
			Reason:      "selected service must define image or build",
		})
	}

	return file, nil
}

func (s *serviceMap) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("services must be a map")
	}
	services := map[string]serviceDocument{}
	for i := 0; i < len(value.Content); i += 2 {
		name := value.Content[i].Value
		var service serviceDocument
		if err := value.Content[i+1].Decode(&service); err != nil {
			return fmt.Errorf("service %q: %w", name, err)
		}
		services[name] = service
	}
	*s = services
	return nil
}

func (l *labelsValue) UnmarshalYAML(value *yaml.Node) error {
	labels := map[string]string{}

	switch value.Kind {
	case yaml.ScalarNode:
		if value.Tag == "!!null" {
			*l = labels
			return nil
		}
		return fmt.Errorf("labels must be a map or list")
	case yaml.MappingNode:
		for i := 0; i < len(value.Content); i += 2 {
			key := value.Content[i].Value
			var labelValue string
			if err := value.Content[i+1].Decode(&labelValue); err != nil {
				return fmt.Errorf("label %q value must be scalar", key)
			}
			labels[key] = labelValue
		}
	case yaml.SequenceNode:
		for _, item := range value.Content {
			var raw string
			if err := item.Decode(&raw); err != nil {
				return fmt.Errorf("label list entries must be strings")
			}
			key, labelValue, ok := splitLabel(raw)
			if !ok {
				labels[raw] = ""
				continue
			}
			labels[key] = labelValue
		}
	default:
		return fmt.Errorf("labels must be a map or list")
	}

	*l = labels
	return nil
}

func splitLabel(value string) (string, string, bool) {
	for i, r := range value {
		if r == '=' {
			return value[:i], value[i+1:], true
		}
	}
	return value, "", false
}
