package config

import (
	"encoding/json"
	"testing"

	"github.com/charmbracelet/crush/internal/imageresize"
	"github.com/stretchr/testify/require"
)

func TestOptionsImageResize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		options  *Options
		expected imageresize.Config
	}{
		{
			name:     "nil options use defaults",
			options:  nil,
			expected: imageresize.DefaultConfig(),
		},
		{
			name:     "unset image uses defaults",
			options:  &Options{},
			expected: imageresize.DefaultConfig(),
		},
		{
			name: "custom overrides win",
			options: &Options{Image: &ImageOptions{
				Enable:      ptr(false),
				MaxWidth:    ptr(1600),
				MaxHeight:   ptr(1200),
				MaxBytes:    ptr(1024 * 1024),
				JpegQuality: []int{90, 60},
			}},
			expected: imageresize.Config{
				Enabled: false,
				Limits: imageresize.Limits{
					MaxWidth:    1600,
					MaxHeight:   1200,
					MaxBytes:    1024 * 1024,
					JpegQuality: []int{90, 60},
				},
			},
		},
		{
			name: "partial overrides keep defaults",
			options: &Options{Image: &ImageOptions{
				MaxWidth: ptr(1000),
			}},
			expected: func() imageresize.Config {
				cfg := imageresize.DefaultConfig()
				cfg.Limits.MaxWidth = 1000
				return cfg
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.options.ImageResize()
			require.Equal(t, tt.expected.Enabled, got.Enabled)
			require.Equal(t, tt.expected.Limits.MaxWidth, got.Limits.MaxWidth)
			require.Equal(t, tt.expected.Limits.MaxHeight, got.Limits.MaxHeight)
			require.Equal(t, tt.expected.Limits.MaxBytes, got.Limits.MaxBytes)
			require.Equal(t, tt.expected.Limits.JpegQuality, got.Limits.JpegQuality)
		})
	}
}

func TestConfigStoreImageResizeNilSafe(t *testing.T) {
	t.Parallel()

	var store *ConfigStore
	require.Equal(t, imageresize.DefaultConfig().Enabled, store.ImageResize().Enabled)
	require.Equal(t, imageresize.DefaultConfig().Limits, store.ImageResize().Limits)
}

func TestImageOptionsFromJSON(t *testing.T) {
	t.Parallel()

	var cfg Config
	require.NoError(t, json.Unmarshal([]byte(`{"options":{"image":{"enable":false,"max_width":1000,"jpeg_quality":[80,55]}}}`), &cfg))
	require.NotNil(t, cfg.Options.Image)
	require.NotNil(t, cfg.Options.Image.Enable)
	require.False(t, *cfg.Options.Image.Enable)
	require.Equal(t, 1000, *cfg.Options.Image.MaxWidth)
	require.Equal(t, []int{80, 55}, cfg.Options.Image.JpegQuality)

	resolved := cfg.Options.ImageResize()
	require.False(t, resolved.Enabled)
	require.Equal(t, 1000, resolved.Limits.MaxWidth)
	require.Equal(t, []int{80, 55}, resolved.Limits.JpegQuality)
	require.Equal(t, imageresize.DefaultMaxHeight, resolved.Limits.MaxHeight)
}
