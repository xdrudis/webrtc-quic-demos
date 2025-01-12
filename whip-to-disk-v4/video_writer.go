package main

import "github.com/pion/rtp"

type VideoWriter interface {
	WriteRTP(packet *rtp.Packet) error
	Close() error
}
