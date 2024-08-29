package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
)

type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type Client struct {
	conn         *websocket.Conn
	peerID       string
	peerConns    map[string]*webrtc.PeerConnection
	dataChannels map[string]*webrtc.DataChannel
}

func NewClient(url string) (*Client, error) {
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return nil, err
	}

	return &Client{
		conn:         conn,
		peerConns:    make(map[string]*webrtc.PeerConnection),
		dataChannels: make(map[string]*webrtc.DataChannel),
	}, nil
}

func (c *Client) Register() {
	msg := Message{
		Type: "register",
	}
	log.Println("Sending register message")
	err := c.conn.WriteJSON(msg)
	if err != nil {
		log.Println("Error sending register message:", err)
		return
	}
	log.Println("Register message sent successfully")
}

func (c *Client) ConnectToPeer(peerID string) error {
	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return err
	}

	dataChannel, err := peerConnection.CreateDataChannel("data", nil)
	if err != nil {
		return err
	}

	c.setupDataChannel(dataChannel, peerID)

	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		return err
	}

	err = peerConnection.SetLocalDescription(offer)
	if err != nil {
		return err
	}

	c.peerConns[peerID] = peerConnection

	offerMsg := Message{
		Type:    "offer",
		Payload: json.RawMessage(fmt.Sprintf(`{"target":"%s","offer":%s}`, peerID, offer.SDP)),
	}
	c.conn.WriteJSON(offerMsg)

	return nil
}

func (c *Client) SendMessage(peerID, message string) {
	if dataChannel, ok := c.dataChannels[peerID]; ok {
		dataChannel.SendText(message)
		fmt.Printf("Message sent to %s: %s\n", peerID, message)
	} else {
		fmt.Printf("No open data channel to peer %s\n", peerID)
	}
}

func (c *Client) setupDataChannel(dataChannel *webrtc.DataChannel, peerID string) {
	dataChannel.OnOpen(func() {
		fmt.Printf("Data channel opened with peer: %s\n", peerID)
		c.dataChannels[peerID] = dataChannel
	})

	dataChannel.OnMessage(func(msg webrtc.DataChannelMessage) {
		fmt.Printf("Received message from %s: %s\n", peerID, string(msg.Data))
	})
}

func (c *Client) handleIncomingMessages() {
	for {
		var msg Message
		err := c.conn.ReadJSON(&msg)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Println("WebSocket connection closed unexpectedly:", err)
			} else {
				log.Println("Error reading message:", err)
			}
			return
		}

		log.Printf("Received message type: %s", msg.Type)
		log.Printf("Full message: %+v", msg)

		switch msg.Type {
		case "registered":
			var payload struct {
				PeerID string `json:"peerId"`
			}
			err = json.Unmarshal(msg.Payload, &payload)
			if err != nil {
				log.Println("Error unmarshalling registered payload:", err)
				continue
			}
			if payload.PeerID == "" {
				log.Println("Received empty peer ID")
				continue
			}
			c.peerID = payload.PeerID
			log.Printf("Registered with peer ID: '%s'", c.peerID)
			fmt.Printf("Registered with peer ID: %s\n", c.peerID)

		case "offer":
			var payload struct {
				Offer  webrtc.SessionDescription `json:"offer"`
				Source string                    `json:"source"`
			}
			json.Unmarshal(msg.Payload, &payload)
			c.handleOffer(payload.Offer, payload.Source)

		case "answer":
			var payload struct {
				Answer webrtc.SessionDescription `json:"answer"`
				Source string                    `json:"source"`
			}
			json.Unmarshal(msg.Payload, &payload)
			c.handleAnswer(payload.Answer, payload.Source)

		case "ice-candidate":
			var payload struct {
				Candidate webrtc.ICECandidateInit `json:"candidate"`
				Source    string                  `json:"source"`
			}
			json.Unmarshal(msg.Payload, &payload)
			c.handleICECandidate(payload.Candidate, payload.Source)
		}
	}
}

func (c *Client) handleOffer(offer webrtc.SessionDescription, sourcePeerID string) {
	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		log.Println("Error creating peer connection:", err)
		return
	}

	peerConnection.OnDataChannel(func(dataChannel *webrtc.DataChannel) {
		c.setupDataChannel(dataChannel, sourcePeerID)
	})

	err = peerConnection.SetRemoteDescription(offer)
	if err != nil {
		log.Println("Error setting remote description:", err)
		return
	}

	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		log.Println("Error creating answer:", err)
		return
	}

	err = peerConnection.SetLocalDescription(answer)
	if err != nil {
		log.Println("Error setting local description:", err)
		return
	}

	c.peerConns[sourcePeerID] = peerConnection

	answerMsg := Message{
		Type:    "answer",
		Payload: json.RawMessage(fmt.Sprintf(`{"target":"%s","answer":%s}`, sourcePeerID, answer.SDP)),
	}
	c.conn.WriteJSON(answerMsg)
}

func (c *Client) handleAnswer(answer webrtc.SessionDescription, sourcePeerID string) {
	if peerConnection, ok := c.peerConns[sourcePeerID]; ok {
		peerConnection.SetRemoteDescription(answer)
	}
}

func (c *Client) handleICECandidate(candidate webrtc.ICECandidateInit, sourcePeerID string) {
	if peerConnection, ok := c.peerConns[sourcePeerID]; ok {
		peerConnection.AddICECandidate(candidate)
	}
}

func main() {
	client, err := NewClient("ws://localhost:8080/ws")
	if err != nil {
		log.Fatal("Error connecting to server:", err)
	}

	go client.handleIncomingMessages()

	fmt.Println("P2P CLI Client")
	fmt.Println("Available commands: register, connect <peerId>, send <peerId> <message>, exit")

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		scanner.Scan()
		input := scanner.Text()
		parts := strings.SplitN(input, " ", 3)

		switch parts[0] {
		case "register":
			client.Register()
		case "connect":
			if len(parts) < 2 {
				fmt.Println("Usage: connect <peerId>")
				continue
			}
			err := client.ConnectToPeer(parts[1])
			if err != nil {
				fmt.Println("Error connecting to peer:", err)
			}
		case "send":
			if len(parts) < 3 {
				fmt.Println("Usage: send <peerId> <message>")
				continue
			}
			client.SendMessage(parts[1], parts[2])
		case "exit":
			return
		default:
			fmt.Println("Unknown command. Available commands: register, connect, send, exit")
		}
	}
}
