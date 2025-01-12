package main

import (
	"fmt"
	"sync"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4/pkg/media/h264writer"
)

type VideoH264Writer struct {
	writer *h264writer.H264Writer
	mu     sync.Mutex
}

func NewVideoH264Writer(filename string) (*VideoH264Writer, error) {
	h264File, err := h264writer.New(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to create H264 writer: %w", err)
	}

	return &VideoH264Writer{
		writer: h264File,
		mu:     sync.Mutex{},
	}, nil
}

func (v *VideoH264Writer) WriteRTP(packet *rtp.Packet) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if err := v.writer.WriteRTP(packet); err != nil {
		return fmt.Errorf("failed to write RTP packet: %w", err)
	}
	return nil
}

func (v *VideoH264Writer) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if err := v.writer.Close(); err != nil {
		return fmt.Errorf("failed to close writer: %w", err)
	}
	return nil
}
