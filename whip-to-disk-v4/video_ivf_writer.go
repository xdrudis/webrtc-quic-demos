package main

import (
	"fmt"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media/ivfwriter"
	"sync"
)

type VideoIvfWriter struct {
	writer *ivfwriter.IVFWriter
	mu     sync.Mutex
}

func NewVideoIvfWriter(codecMimeType string, filename string) (*VideoIvfWriter, error) {
	ivfFile, err := ivfwriter.New(filename, ivfwriter.WithCodec(codecMimeType))
	if err != nil {
		return nil, fmt.Errorf("failed to create IVF writer: %w", err)
	}

	return &VideoIvfWriter{
		writer: ivfFile,
		mu:     sync.Mutex{},
	}, nil
}

func (v *VideoIvfWriter) WriteRTP(packet *rtp.Packet) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if err := v.writer.WriteRTP(packet); err != nil {
		return fmt.Errorf("failed to write RTP packet: %w", err)
	}
	return nil
}

func (v *VideoIvfWriter) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if err := v.writer.Close(); err != nil {
		return fmt.Errorf("failed to close writer: %w", err)
	}
	return nil
}

func getCodecWriter(codec webrtc.RTPCodecParameters) (VideoWriter, error) {
	switch codec.MimeType {
	case webrtc.MimeTypeVP8, webrtc.MimeTypeAV1:
		filename := fmt.Sprintf("recordings/%s.ivf", codec.MimeType[6:])
		writer, err := NewVideoIvfWriter(codec.MimeType, filename)
		if err != nil {
			return nil, fmt.Errorf("failed to create IVF writer: %w", err)
		}
		return writer, nil

	case webrtc.MimeTypeH264:
		filename := fmt.Sprintf("recordings/%s.h264", codec.MimeType[6:])
		writer, err := NewVideoH264Writer(filename)
		if err != nil {
			return nil, fmt.Errorf("failed to create H264 writer: %w", err)
		}
		return writer, nil

	default:
		return nil, fmt.Errorf("unsupported codec: %s", codec.MimeType)
	}
}
