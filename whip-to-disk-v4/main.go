package main

import (
	"errors"
	"fmt"
	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/intervalpli"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"io"
	"net/http"
)

var (
	peerConnectionConfiguration = webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	}
)

func main() {
	http.Handle("/", http.FileServer(http.Dir(".")))
	http.HandleFunc("/whip", whipHandler)

	const listenPort = ":8081"
	fmt.Println("Starting server at http://localhost" + listenPort)
	panic(http.ListenAndServe(listenPort, nil))
}

func whipHandler(w http.ResponseWriter, r *http.Request) {
	offer, err := io.ReadAll(r.Body)
	if err != nil {
		panic(err)
	}

	// Create a MediaEngine object to configure the supported codecs
	m := &webrtc.MediaEngine{}
	for _, codec := range videoCodecs {
		if err := m.RegisterCodec(codec, webrtc.RTPCodecTypeVideo); err != nil {
			panic(err)
		}
	}

	// Create an InterceptorRegistry
	i := &interceptor.Registry{}

	// Register intervalpli factory
	intervalPliFactory, err := intervalpli.NewReceiverInterceptor()
	if err != nil {
		panic(err)
	}
	i.Add(intervalPliFactory)

	if err = webrtc.RegisterDefaultInterceptors(m, i); err != nil {
		panic(err)
	}

	// Create the API object with the MediaEngine
	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(i))

	// Create a new RTCPeerConnection
	peerConnection, err := api.NewPeerConnection(peerConnectionConfiguration)
	if err != nil {
		panic(err)
	}

	// Allow us to receive 1 video track
	videoTransceiver, err := peerConnection.AddTransceiverFromKind(
		webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{
			Direction: webrtc.RTPTransceiverDirectionRecvonly,
		},
	)
	if err != nil {
		panic(err)
	}

	// m.RegisterDefaultCodecs() is not enough to make the codecs available to the transceiver.
	err = videoTransceiver.SetCodecPreferences(videoCodecs)
	if err != nil {
		panic(err)
	}

	// Handle incoming video tracks
	peerConnection.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		fmt.Printf("Track parameters - MIME: %s, SSRC: %d, PayloadType: %d\n",
			remoteTrack.Codec().MimeType,
			remoteTrack.SSRC(),
			remoteTrack.PayloadType())

		// Get the track's parameters
		params := remoteTrack.Codec().RTPCodecCapability
		fmt.Printf("Codec parameters: %+v\n", params)

		writer, err := getCodecWriter(remoteTrack.Codec())
		if err != nil {
			panic(err)
		}
		defer writer.Close()

		for {
			pkt, _, err := remoteTrack.ReadRTP()
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				fmt.Printf("%v\n", err)
				panic(err)
			}
			parseVP8Packet(pkt)
			if err := writer.WriteRTP(pkt); err != nil {
				panic(err)
			}
		}
	})

	writeAnswer(w, peerConnection, offer, "/whip")
}

func isKeyFrameAndGetDimensions(p *codecs.VP8Packet) (bool, int, int) {
	// A keyframe requires enough bytes for the frame tag (3 bytes),
	// sync code (3 bytes), and width/height info (4 bytes)
	const keyframeMinLength = 10
	if len(p.Payload) < keyframeMinLength {
		return false, 0, 0
	}

	// The low bit of the first byte of the raw VP8 data (frame tag)
	// must be 0 for a keyframe.
	// p.Payload[0] is the first byte of the VP8 bitstream (after the VP8 payload descriptor).
	if (p.Payload[0] & 0x01) != 0 {
		return false, 0, 0
	}

	// Next 3 bytes should match the VP8 keyframe start/sync code: 0x9D 0x01 0x2A
	if p.Payload[3] != 0x9D || p.Payload[4] != 0x01 || p.Payload[5] != 0x2A {
		return false, 0, 0
	}

	// Bytes 6–7: Frame width (14 bits) + horizontal scale (2 bits)
	// Bytes 8–9: Frame height (14 bits) + vertical scale (2 bits)
	// Mask out the lower 14 bits to ignore scale.
	width := (uint16(p.Payload[6]) | (uint16(p.Payload[7]) << 8)) & 0x3FFF
	height := (uint16(p.Payload[8]) | (uint16(p.Payload[9]) << 8)) & 0x3FFF

	return true, int(width), int(height)
}

func parseVP8Packet(packet *rtp.Packet) (*codecs.VP8Packet, error) {
	if packet == nil {
		return nil, fmt.Errorf("packet is nil")
	}

	vp8Packet := &codecs.VP8Packet{}

	// Parse the payload into the VP8 packet structure
	_, err := vp8Packet.Unmarshal(packet.Payload)
	if err != nil {
		return nil, fmt.Errorf("failed to parse VP8 payload: %v", err)
	}
	if isKeyframe, width, height := isKeyFrameAndGetDimensions(vp8Packet); isKeyframe {
		fmt.Printf("Keyframe: %d x %d\n", width, height)
	}
	return vp8Packet, nil
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

	fmt.Printf("Offer type: %s\n", sdp.Type.String())
	fmt.Printf("SDP content:\n%s\n", sdp.SDP)

	if err := peerConnection.SetRemoteDescription(sdp); err != nil {
		panic(err)
	}

	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		panic(err)
	}

	if err = peerConnection.SetLocalDescription(answer); err != nil {
		fmt.Printf("%v\n", err)
		panic(err)
	}

	<-gatherComplete

	w.Header().Add("Location", path)
	w.WriteHeader(http.StatusCreated)
	fmt.Fprint(w, peerConnection.LocalDescription().SDP)
}
