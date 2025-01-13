package main

import (
	"fmt"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media/ivfreader"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

var (
	// Track that will be shared with all WHEP clients
	videoTrack *webrtc.TrackLocalStaticRTP

	// Mutex to protect first client detection
	mu                  sync.Mutex
	hasStartedStreaming bool

	// Keep file handle and reader at package level
	ivfFile   *os.File
	ivfReader *ivfreader.IVFReader

	peerConnectionConfiguration = webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	}
)

var sequenceNumber uint16 = 0

func startStreamingIVF(filename string) error {
	// Check if file exists first
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return fmt.Errorf("IVF file does not exist: %s", filename)
	}

	var err error
	ivfFile, err = os.Open(filename)
	if err != nil {
		return fmt.Errorf("failed to open IVF file: %v", err)
	}

	// Create new IVF reader
	var header *ivfreader.IVFFileHeader
	ivfReader, header, err = ivfreader.NewWith(ivfFile)
	if err != nil {
		ivfFile.Close()
		return fmt.Errorf("failed to create IVF reader: %v", err)
	}

	fmt.Printf("IVF File Info: %dx%d @ %d fps\n",
		header.Width,
		header.Height,
		header.TimebaseDenominator/header.TimebaseNumerator)

	// Calculate frame duration based on timebase
	frameDuration := time.Duration(float64(header.TimebaseNumerator) / float64(header.TimebaseDenominator) * float64(time.Second))

	go func() {
		payloader := &codecs.VP8Payloader{}
		//sampleRate := uint32(90000) // WebRTC default
		const ivfFileHeaderSize = 32

		for {
			// Read next frame
			frameData, frameHeader, err := ivfReader.ParseNextFrame()
			if err != nil {
				if err == io.EOF {
					// Reset the reader to the beginning of the file
					ivfFile.Seek(int64(ivfFileHeaderSize), 0)
					ivfReader.ResetReader(func(bytesRead int64) io.Reader { return ivfFile })
					continue
				}
				fmt.Printf("Error reading frame: %v\n", err)
				return
			}

			// Split frame into RTP packets
			payloads := payloader.Payload(1200, frameData)
			for i, payload := range payloads {
				timestamp := uint32(frameHeader.Timestamp * 90000 / uint64(header.TimebaseDenominator))
				packet := &rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						PayloadType:    96,             // typical for VP8
						SequenceNumber: sequenceNumber, // will be set by Track
						Timestamp:      timestamp,
						SSRC:           0,                    // will be set by Track
						Marker:         i == len(payloads)-1, // true for last packet in frame
					},
					Payload: payload,
				}
				sequenceNumber++

				buf, err := packet.Marshal()
				if err != nil {
					fmt.Printf("Error marshaling RTP packet: %v\n", err)
					continue
				}

				if _, err := videoTrack.Write(buf); err != nil {
					fmt.Printf("Error writing RTP packet: %v\n", err)
					return
				}
			}

			time.Sleep(frameDuration)
		}
	}()

	return nil
}

var api *webrtc.API

func init() {
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterCodec(
		webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:     webrtc.MimeTypeVP8,
				ClockRate:    90000,
				Channels:     0,
				SDPFmtpLine:  "",
				RTCPFeedback: nil,
			},
			PayloadType: 96,
		},
		webrtc.RTPCodecTypeVideo,
	); err != nil {
		panic(err)
	}

	// Create API with the MediaEngine
	api = webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine))
}

func main() {
	// Create the video track that will be shared with all viewers

	var err error
	if videoTrack, err = webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8},
		"video",
		"pion",
	); err != nil {
		panic(err)
	}

	// Cleanup on exit
	defer func() {
		if ivfFile != nil {
			ivfFile.Close()
		}
	}()

	http.Handle("/", http.FileServer(http.Dir(".")))
	http.HandleFunc("/whep", whepHandler)

	const listenPort = ":8082"
	fmt.Println("Starting WHEP viewer server at http://localhost" + listenPort)
	panic(http.ListenAndServe(listenPort, nil))
}

func whepHandler(w http.ResponseWriter, r *http.Request) {
	// Start streaming for first client
	mu.Lock()
	if !hasStartedStreaming {
		if err := startStreamingIVF("recordings/VP8.ivf"); err != nil {
			fmt.Printf("Failed to start streaming: %v\n", err)
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(err.Error()))
			mu.Unlock()
			return
		}
		hasStartedStreaming = true
	}
	mu.Unlock()

	// Read the offer from HTTP Request
	offer, err := io.ReadAll(r.Body)
	if err != nil {
		panic(err)
	}

	// Create a new RTCPeerConnection
	peerConnection, err := api.NewPeerConnection(peerConnectionConfiguration)
	if err != nil {
		panic(err)
	}

	// Add the shared video track to this peer connection
	rtpSender, err := peerConnection.AddTrack(videoTrack)
	if err != nil {
		panic(err)
	}

	// Process incoming RTCP packets for this sender
	go func() {
		rtcpBuf := make([]byte, 1500)
		for {
			if _, _, rtcpErr := rtpSender.Read(rtcpBuf); rtcpErr != nil {
				return
			}
		}
	}()

	writeAnswer(w, peerConnection, offer, "/whep")
}

func writeAnswer(w http.ResponseWriter, peerConnection *webrtc.PeerConnection, offer []byte, path string) {
	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		fmt.Printf("ICE Connection State has changed: %s\n", connectionState.String())
		if connectionState == webrtc.ICEConnectionStateFailed {
			_ = peerConnection.Close()
		}
	})

	sdp := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  string(offer),
	}

	if err := peerConnection.SetRemoteDescription(sdp); err != nil {
		panic(err)
	}

	// Create channel that is blocked until ICE Gathering is complete
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

	// Create answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		panic(err)
	}

	if err = peerConnection.SetLocalDescription(answer); err != nil {
		panic(err)
	}

	// Block until ICE Gathering is complete
	<-gatherComplete

	w.Header().Add("Location", path)
	w.WriteHeader(http.StatusCreated)
	fmt.Fprint(w, peerConnection.LocalDescription().SDP)
}
