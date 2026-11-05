package dlp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Disaldr/go-dlp/media"
	"github.com/Disaldr/go-dlp/postprocess"
)

func audioCodec(info *media.Info) string {
	for _, f := range info.RequestedFormats {
		if f.IsAudioOnly() && f.ACodec != "" {
			return f.ACodec
		}
	}
	for _, f := range info.RequestedFormats {
		if f.HasAudio() && f.ACodec != "" {
			return f.ACodec
		}
	}
	return ""
}

func (c *Client) outputExt(info *media.Info, co callOpts) string {
	if co.extractAudio {
		ext, _ := postprocess.AudioTarget(co.audioFormat, audioCodec(info))
		return ext
	}
	if co.remuxVideo != "" {
		switch co.remuxVideo {
		case "alac":
			return "m4a"
		case "vorbis":
			return "ogg"
		}
		return co.remuxVideo
	}
	return info.Ext
}

func replaceExt(path, old, ext string) string {
	if old == ext {
		return path
	}
	return strings.TrimSuffix(path, "."+old) + "." + ext
}

func (c *Client) postprocess(ctx context.Context, info *media.Info, co callOpts) error {
	if !co.extractAudio && co.remuxVideo == "" {
		return nil
	}
	ext := c.outputExt(info, co)
	encoder := ""
	if co.extractAudio {
		if c.ffmpeg == nil {
			return fmt.Errorf("extract audio: %w", postprocess.ErrNoFFmpeg)
		}
		codec, err := c.ffmpeg.AudioCodec(ctx, info.Filename)
		if err != nil {
			return err
		}
		ext, encoder = postprocess.AudioTarget(co.audioFormat, codec)
	} else if ext == info.Ext {
		return nil
	}
	target := replaceExt(info.Filename, info.Ext, ext)
	if target != info.Filename && !co.overwrite && exists(target) {
		info.Filename, info.Ext = target, ext
		return nil
	}
	// Пишем рядом с итоговым файлом и заменяем его только при успехе.
	tmp, err := os.CreateTemp(filepath.Dir(target), ".go-dlp-*."+ext)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	defer os.Remove(tmpPath)
	if co.extractAudio {
		c.log.Info("extracting audio", "file", target)
		err = c.ffmpeg.ExtractAudio(ctx, info.Filename, tmpPath, encoder)
	} else {
		c.log.Info("remuxing video", "file", target)
		err = c.merger.Remux(ctx, info.Filename, tmpPath)
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return err
	}
	if target != info.Filename {
		if err := os.Remove(info.Filename); err != nil {
			c.log.Warn("removing intermediate file", "err", err)
		}
	}
	info.Filename, info.Ext = target, ext
	return nil
}
